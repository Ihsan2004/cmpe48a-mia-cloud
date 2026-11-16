package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

type CorrectionService struct {
	transcriptRepo repository.TranscriptRepository
	dictRepo       repository.CorrectionDictionaryRepository
	meetingRepo    repository.MeetingRepository
	usageRecorder  *UsageRecorder
	client         *http.Client
	apiKey         string
	batchSize      int // Batch size for GPT requests (number of blocks)
	maxConcurrency int
	meetingLocks   sync.Map

	// Dictionary cache
	dictCache    []repository.CorrectionTerm
	dictCachedAt time.Time
	dictMu       sync.RWMutex

	// Circuit breaker for OpenAI API
	cbMu             sync.RWMutex
	cbConsecutiveFail int
	cbOpenUntil      time.Time // circuit stays open (reject calls) until this time
}

const dictCacheTTL = 5 * time.Minute

const (
	streamingTailOverlapSeconds = 12.0
	finalizationTailSeconds     = 90.0

	// Retry settings
	correctionMaxRetries = 2
	correctionModel      = "gpt-5.4"

	// Circuit breaker settings
	// After cbFailThreshold consecutive failures, circuit opens.
	// Open duration doubles each time: 30s → 60s → 120s → ... up to cbMaxOpenDuration.
	// A single success resets the circuit to closed.
	cbFailThreshold   = 3
	cbBaseOpenDuration = 30 * time.Second
	cbMaxOpenDuration  = 5 * time.Minute
)

func NewCorrectionService() *CorrectionService {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		slog.Warn("OPENAI_API_KEY not found, correction service may fail")
	}
	return &CorrectionService{
		transcriptRepo: repository.NewTranscriptRepository(),
		dictRepo:       repository.NewCorrectionDictionaryRepository(),
		meetingRepo:    repository.NewMeetingRepository(),
		usageRecorder:  NewUsageRecorder(),
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		apiKey:         apiKey,
		batchSize:      30, // Process 30 *blocks* at a time (reduced from 50 for better quality)
		maxConcurrency: 5,
	}
}

// --- Circuit Breaker ---
// Implements a simple circuit breaker with exponential open duration.
// States: CLOSED (normal) → OPEN (reject) → HALF-OPEN (allow one probe).

// cbIsOpen returns true if the circuit breaker is open (should reject calls).
func (s *CorrectionService) cbIsOpen() bool {
	s.cbMu.RLock()
	defer s.cbMu.RUnlock()
	if s.cbOpenUntil.IsZero() {
		return false
	}
	return time.Now().Before(s.cbOpenUntil)
}

// cbRecordSuccess resets the circuit breaker to closed state.
func (s *CorrectionService) cbRecordSuccess() {
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	if s.cbConsecutiveFail > 0 {
		slog.Info("Circuit breaker: closed (OpenAI recovered)", "previous_failures", s.cbConsecutiveFail)
	}
	s.cbConsecutiveFail = 0
	s.cbOpenUntil = time.Time{}
}

// cbRecordFailure increments the failure counter and opens the circuit if threshold reached.
func (s *CorrectionService) cbRecordFailure() {
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	s.cbConsecutiveFail++
	if s.cbConsecutiveFail >= cbFailThreshold {
		// Exponential open duration: 30s, 60s, 120s, 240s, 300s (cap)
		multiplier := s.cbConsecutiveFail / cbFailThreshold // 1, 2, 3, ...
		openDuration := cbBaseOpenDuration * time.Duration(1<<uint(multiplier-1))
		if openDuration > cbMaxOpenDuration {
			openDuration = cbMaxOpenDuration
		}
		s.cbOpenUntil = time.Now().Add(openDuration)
		slog.Warn("Circuit breaker: OPEN — pausing OpenAI correction calls",
			"consecutive_failures", s.cbConsecutiveFail,
			"open_duration", openDuration,
		)
	}
}

type BlockCorrectionRequestItem struct {
	ID   string `json:"id"` // Block ID (temp)
	Text string `json:"text"`
}

type BlockCorrectionResponseItem struct {
	ID            string `json:"id"`
	CorrectedText string `json:"corrected_text"`
}

// Temporary struct for aggregation
type aggregatedBlock struct {
	Speaker           string
	Segments          []models.TranscriptSegment
	StartTime         float64
	EndTime           float64
	OriginalText      string
	SpeakerPropagated bool // True if ANY segment was propagated (Legacy)
	PropagatedCount   int  // Number of propagated segments in this block (FIX 2.3)
}

// ProcessMeetingTranscript keeps backward compatibility and now runs short finalization mode.
func (s *CorrectionService) ProcessMeetingTranscript(meetingID uuid.UUID) error {
	return s.ProcessMeetingTranscriptFinalization(meetingID)
}

func (s *CorrectionService) withMeetingLock(meetingID uuid.UUID, fn func() error) error {
	lockKey := meetingID.String()
	lockValue, _ := s.meetingLocks.LoadOrStore(lockKey, &sync.Mutex{})
	mu := lockValue.(*sync.Mutex)

	mu.Lock()
	defer mu.Unlock()

	return fn()
}

// getDictionaryTerms returns cached correction dictionary terms, refreshing if TTL expired.
func (s *CorrectionService) getDictionaryTerms() []repository.CorrectionTerm {
	s.dictMu.RLock()
	if s.dictCache != nil && time.Since(s.dictCachedAt) < dictCacheTTL {
		terms := s.dictCache
		s.dictMu.RUnlock()
		return terms
	}
	s.dictMu.RUnlock()

	s.dictMu.Lock()
	defer s.dictMu.Unlock()

	// Double-check after acquiring write lock
	if s.dictCache != nil && time.Since(s.dictCachedAt) < dictCacheTTL {
		return s.dictCache
	}

	terms, err := s.dictRepo.GetActiveTerms(context.Background())
	if err != nil {
		slog.Warn("Failed to load correction dictionary, using cache or empty", "error", err)
		if s.dictCache != nil {
			return s.dictCache
		}
		return nil
	}

	s.dictCache = terms
	s.dictCachedAt = time.Now()
	slog.Info("Correction dictionary loaded", "active_terms", len(terms))
	return terms
}

// buildDomainTermsPrompt generates the DOMAIN TERİMLERİ section from the dictionary.
func (s *CorrectionService) buildDomainTermsPrompt() string {
	terms := s.getDictionaryTerms()
	if len(terms) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("DOMAIN TERİMLERİ (sık karşılaşılan STT hataları):\n")
	for _, t := range terms {
		sb.WriteString(fmt.Sprintf("- '%s' → '%s'", t.WrongForm, t.CorrectForm))
		if t.Description != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", t.Description))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (s *CorrectionService) meetingContext(meetingID uuid.UUID) (string, []string) {
	meetingTitle, participants, err := s.transcriptRepo.GetMeetingMetadata(context.Background(), meetingID)
	if err != nil {
		slog.Warn("Could not fetch meeting metadata, continuing without context", "error", err)
		return "", []string{}
	}
	slog.Info("Meeting context loaded", "meeting_title", meetingTitle, "participant_count", len(participants))
	return meetingTitle, participants
}

func (s *CorrectionService) buildCorrectedBlocks(
	meetingID uuid.UUID,
	segments []models.TranscriptSegment,
	meetingTitle string,
	participants []string,
) ([]models.TranscriptChatBlock, correctionTokenUsage, error) {
	if len(segments) == 0 {
		return nil, correctionTokenUsage{}, nil
	}

	segments = deduplicateSegments(segments)
	slog.Info("After deduplication", "segment_count", len(segments))

	segments, propagatedIDs := propagateSpeakers(segments)
	if len(propagatedIDs) > 0 {
		slog.Info("Propagated speaker", "segment_count", len(propagatedIDs))
	}

	aggBlocks := s.aggregateSegments(segments, propagatedIDs)
	slog.Info("Aggregated segments into blocks", "segment_count", len(segments), "block_count", len(aggBlocks))

	aggBlocks = filterEmptyBlocks(aggBlocks)
	if len(aggBlocks) == 0 {
		slog.Warn("No blocks remaining after filtering empty ones")
		return nil, correctionTokenUsage{}, nil
	}
	slog.Info("After filtering empty blocks", "block_count", len(aggBlocks))

	var batches [][]aggregatedBlock
	for i := 0; i < len(aggBlocks); i += s.batchSize {
		end := i + s.batchSize
		if end > len(aggBlocks) {
			end = len(aggBlocks)
		}
		batches = append(batches, aggBlocks[i:end])
	}

	type batchResult struct {
		blocks []models.TranscriptChatBlock
		usage  correctionTokenUsage
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, s.maxConcurrency)
	resultsChan := make(chan batchResult, len(batches))
	errChan := make(chan error, len(batches))

	for i, batch := range batches {
		wg.Add(1)
		go func(idx int, b []aggregatedBlock) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			correctedBlocks, usage, err := s.processBatch(meetingID, b, meetingTitle, participants)
			if err != nil {
				slog.Error("Batch failed", "batch", idx+1, "error", err)
				errChan <- err
				return
			}
			resultsChan <- batchResult{blocks: correctedBlocks, usage: usage}
			slog.Info("Batch completed", "batch", idx+1, "total_batches", len(batches))
		}(i, batch)
	}

	wg.Wait()
	close(resultsChan)
	close(errChan)

	if len(errChan) > 0 {
		return nil, correctionTokenUsage{}, <-errChan
	}

	var allChatBlocks []models.TranscriptChatBlock
	var totalUsage correctionTokenUsage
	for res := range resultsChan {
		allChatBlocks = append(allChatBlocks, res.blocks...)
		totalUsage.InputTokens += res.usage.InputTokens
		totalUsage.OutputTokens += res.usage.OutputTokens
	}

	sort.Slice(allChatBlocks, func(i, j int) bool {
		return allChatBlocks[i].StartTime < allChatBlocks[j].StartTime
	})

	return allChatBlocks, totalUsage, nil
}

// ProcessMeetingTranscriptIncremental updates chat blocks during live meetings with tail overlap rebuild.
func (s *CorrectionService) ProcessMeetingTranscriptIncremental(meetingID uuid.UUID) error {
	return s.withMeetingLock(meetingID, func() error {
		slog.Info("Incremental transcript update started", "meeting_id", meetingID)
		ctx := context.Background()

		existingBlocks, err := s.transcriptRepo.GetChatBlocksByMeetingID(ctx, meetingID)
		if err != nil {
			return fmt.Errorf("failed to fetch existing chat blocks: %w", err)
		}

		rebuildFrom := 0.0
		if len(existingBlocks) > 0 {
			lastEnd := existingBlocks[len(existingBlocks)-1].EndTime
			rebuildFrom = lastEnd - streamingTailOverlapSeconds
			if rebuildFrom < 0 {
				rebuildFrom = 0
			}
		}

		segments, err := s.transcriptRepo.GetSegmentsByMeetingIDFromTime(ctx, meetingID, rebuildFrom)
		if err != nil {
			return fmt.Errorf("failed to fetch incremental segments: %w", err)
		}
		if len(segments) == 0 {
			return nil
		}

		// Skip work if transcript has not advanced since last known block end.
		if len(existingBlocks) > 0 {
			lastBlockEnd := existingBlocks[len(existingBlocks)-1].EndTime
			lastSegmentEnd := segments[len(segments)-1].EndTime
			if lastSegmentEnd <= lastBlockEnd+0.1 {
				return nil
			}
		}

		meetingTitle, participants := s.meetingContext(meetingID)
		correctedBlocks, tokenUsage, err := s.buildCorrectedBlocks(meetingID, segments, meetingTitle, participants)
		if err != nil {
			return fmt.Errorf("failed to build incremental corrected blocks: %w", err)
		}

		// Record correction usage
		if tokenUsage.InputTokens > 0 || tokenUsage.OutputTokens > 0 {
			s.recordCorrectionUsage(meetingID, tokenUsage)
		}

		if len(correctedBlocks) == 0 {
			return nil
		}

		if err := s.transcriptRepo.DeleteChatBlocksFromTime(ctx, meetingID, rebuildFrom); err != nil {
			return fmt.Errorf("failed to cleanup incremental tail blocks: %w", err)
		}

		for i := range correctedBlocks {
			correctedBlocks[i].IsFinalized = false
			correctedBlocks[i].FinalizedAt = nil
			correctedBlocks[i].SourceSegmentCount = nil
		}

		if err := s.transcriptRepo.CreateChatBlocks(ctx, correctedBlocks); err != nil {
			return fmt.Errorf("failed to save incremental chat blocks: %w", err)
		}

		// Generate embeddings for stable blocks (before tail overlap zone)
		if rebuildFrom > 0 {
			go func(mID uuid.UUID, cutoff float64) {
				postMeetingService := NewPostMeetingService()
				postMeetingService.GenerateEmbeddingsForStableBlocks(context.Background(), mID, cutoff)
			}(meetingID, rebuildFrom)
		}

		slog.Info("Incremental transcript update completed", "meeting_id", meetingID, "rebuild_from_s", rebuildFrom, "block_count", len(correctedBlocks))
		return nil
	})
}

// ProcessMeetingTranscriptFinalization performs a short tail reprocess and marks blocks finalized.
func (s *CorrectionService) ProcessMeetingTranscriptFinalization(meetingID uuid.UUID) error {
	return s.withMeetingLock(meetingID, func() error {
		slog.Info("Finalization transcript pass started", "meeting_id", meetingID)
		ctx := context.Background()

		allSegments, err := s.transcriptRepo.GetSegmentsByMeetingID(ctx, meetingID)
		if err != nil {
			return fmt.Errorf("failed to fetch segments: %w", err)
		}
		if len(allSegments) == 0 {
			slog.Warn("No transcript segments found for meeting", "meeting_id", meetingID)
			return nil
		}

		existingBlocks, err := s.transcriptRepo.GetChatBlocksByMeetingID(ctx, meetingID)
		if err != nil {
			return fmt.Errorf("failed to fetch existing chat blocks for finalization: %w", err)
		}

		rebuildFrom := 0.0
		if len(existingBlocks) > 0 {
			lastEnd := existingBlocks[len(existingBlocks)-1].EndTime
			rebuildFrom = lastEnd - finalizationTailSeconds
			if rebuildFrom < 0 {
				rebuildFrom = 0
			}
		}

		tailSegments := allSegments
		if len(existingBlocks) > 0 && rebuildFrom > 0 {
			tailSegments, err = s.transcriptRepo.GetSegmentsByMeetingIDFromTime(ctx, meetingID, rebuildFrom)
			if err != nil {
				return fmt.Errorf("failed to fetch finalization tail segments: %w", err)
			}
			if len(tailSegments) == 0 {
				tailSegments = allSegments
				rebuildFrom = 0
			}
		}

		meetingTitle, participants := s.meetingContext(meetingID)
		correctedBlocks, tokenUsage, err := s.buildCorrectedBlocks(meetingID, tailSegments, meetingTitle, participants)
		if err != nil {
			return fmt.Errorf("failed to build finalized chat blocks: %w", err)
		}

		// Record correction usage
		if tokenUsage.InputTokens > 0 || tokenUsage.OutputTokens > 0 {
			s.recordCorrectionUsage(meetingID, tokenUsage)
		}

		finalizedAt := time.Now().UTC()
		sourceSegmentCount := len(allSegments)

		if len(correctedBlocks) > 0 {
			if len(existingBlocks) == 0 || rebuildFrom <= 0 {
				if err := s.transcriptRepo.DeleteChatBlocksByMeetingID(ctx, meetingID); err != nil {
					return fmt.Errorf("failed to cleanup existing chat blocks: %w", err)
				}
			} else {
				if err := s.transcriptRepo.DeleteChatBlocksFromTime(ctx, meetingID, rebuildFrom); err != nil {
					return fmt.Errorf("failed to cleanup finalization tail blocks: %w", err)
				}
			}

			for i := range correctedBlocks {
				correctedBlocks[i].IsFinalized = true
				correctedBlocks[i].FinalizedAt = &finalizedAt
				correctedBlocks[i].SourceSegmentCount = &sourceSegmentCount
			}

			if err := s.transcriptRepo.CreateChatBlocks(ctx, correctedBlocks); err != nil {
				return fmt.Errorf("failed to save finalized chat blocks: %w", err)
			}
		}

		if err := s.transcriptRepo.MarkChatBlocksFinalized(ctx, meetingID, finalizedAt, sourceSegmentCount); err != nil {
			return fmt.Errorf("failed to mark chat blocks finalized: %w", err)
		}

		// Trigger embedding generation once final transcript is stable.
		go func(mID uuid.UUID) {
			postMeetingService := NewPostMeetingService()
			postMeetingService.GenerateEmbeddingsForMeeting(context.Background(), mID)
		}(meetingID)

		slog.Info("Finalization transcript pass completed", "meeting_id", meetingID, "rebuild_from_s", rebuildFrom, "tail_block_count", len(correctedBlocks))
		return nil
	})
}

func (s *CorrectionService) aggregateSegments(segments []models.TranscriptSegment, propagatedIDs map[uuid.UUID]bool) []aggregatedBlock {
	var blocks []aggregatedBlock
	if len(segments) == 0 {
		return blocks
	}

	const (
		MaxBlockDuration = 15.0 // seconds (reduced from 30s for better context preservation)
		MaxSilenceGap    = 2.0  // seconds
	)

	var currentSegments []models.TranscriptSegment
	currentSpeaker := segments[0].Speaker

	for _, seg := range segments {
		shouldSplit := false

		// 1. Speaker Change
		if seg.Speaker != currentSpeaker {
			shouldSplit = true
		}

		// 2. Check limits if we have current segments
		if !shouldSplit && len(currentSegments) > 0 {
			firstSeg := currentSegments[0]
			lastSeg := currentSegments[len(currentSegments)-1]

			// Duration Check
			// Calculate if adding this segment would exceed max duration
			// (Use seg.EndTime - firstSeg.StartTime)
			if (seg.EndTime - firstSeg.StartTime) > MaxBlockDuration {
				shouldSplit = true
			}

			// Silence Gap Check
			// If gap between this segment and previous one > MaxSilenceGap
			if (seg.StartTime - lastSeg.EndTime) > MaxSilenceGap {
				shouldSplit = true
			}
		}

		if shouldSplit {
			// Commit current block
			if len(currentSegments) > 0 {
				blocks = append(blocks, s.createAggregatedBlock(currentSegments, propagatedIDs))
			}
			// Start new block
			currentSegments = []models.TranscriptSegment{seg}
			currentSpeaker = seg.Speaker
		} else {
			currentSegments = append(currentSegments, seg)
		}
	}
	// Commit last block
	if len(currentSegments) > 0 {
		blocks = append(blocks, s.createAggregatedBlock(currentSegments, propagatedIDs))
	}

	return blocks
}

func (s *CorrectionService) createAggregatedBlock(segs []models.TranscriptSegment, propagatedIDs map[uuid.UUID]bool) aggregatedBlock {
	fullText := ""
	propagatedCount := 0
	for i, seg := range segs {
		if i > 0 {
			fullText += " "
		}
		fullText += seg.Text
		// Check if any segment in this block had its speaker propagated
		if propagatedIDs[seg.ID] {
			propagatedCount++
		}
	}
	return aggregatedBlock{
		Speaker:           segs[0].Speaker,
		Segments:          segs,
		StartTime:         segs[0].StartTime,
		EndTime:           segs[len(segs)-1].EndTime,
		OriginalText:      fullText,
		SpeakerPropagated: propagatedCount > 0,
		PropagatedCount:   propagatedCount,
	}
}

// propagateSpeakers copies speaker from previous segment when current segment has no speaker
// Returns the modified segments and a set of segment IDs that were propagated
func propagateSpeakers(segments []models.TranscriptSegment) ([]models.TranscriptSegment, map[uuid.UUID]bool) {
	propagatedIDs := make(map[uuid.UUID]bool)
	if len(segments) == 0 {
		return segments, propagatedIDs
	}

	lastKnownSpeaker := ""

	for i := range segments {
		currentSpeaker := strings.TrimSpace(segments[i].Speaker)

		if currentSpeaker != "" && currentSpeaker != "unknown" {
			// Valid speaker - update last known
			lastKnownSpeaker = currentSpeaker
		} else if lastKnownSpeaker != "" {
			// Empty speaker - propagate from previous (limit to 1 segment back as per roadmap)
			// Only propagate if the previous segment had a valid speaker
			if i > 0 {
				prevSpeaker := strings.TrimSpace(segments[i-1].Speaker)
				if prevSpeaker != "" && prevSpeaker != "unknown" {
					segments[i].Speaker = prevSpeaker
					propagatedIDs[segments[i].ID] = true
				}
			}
		}
	}

	return segments, propagatedIDs
}

// deduplicateSegments removes duplicate segments (same timestamp + text)
func deduplicateSegments(segments []models.TranscriptSegment) []models.TranscriptSegment {
	seen := make(map[string]bool)
	var unique []models.TranscriptSegment

	for _, seg := range segments {
		// Create key from timestamp and text
		key := fmt.Sprintf("%.3f_%s", seg.StartTime, seg.Text)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, seg)
		}
	}

	return unique
}

// filterEmptyBlocks removes blocks with empty or whitespace-only text
func filterEmptyBlocks(blocks []aggregatedBlock) []aggregatedBlock {
	var filtered []aggregatedBlock
	for _, block := range blocks {
		trimmed := ""
		for _, seg := range block.Segments {
			trimmed += seg.Text
		}
		if len(trimmed) > 0 {
			filtered = append(filtered, block)
		}
	}
	return filtered
}

func (s *CorrectionService) processBatch(meetingID uuid.UUID, batch []aggregatedBlock, meetingTitle string, participants []string) ([]models.TranscriptChatBlock, correctionTokenUsage, error) {
	// Prepare input for prompt
	var inputItems []BlockCorrectionRequestItem
	blockIDMap := make(map[string]aggregatedBlock) // temporary ID -> Block

	for i, block := range batch {
		tempID := fmt.Sprintf("block_%d", i) // Simple temp ID
		inputItems = append(inputItems, BlockCorrectionRequestItem{
			ID:   tempID,
			Text: block.OriginalText,
		})
		blockIDMap[tempID] = block
	}

	inputJSON, _ := json.Marshal(inputItems)

	// Build context header
	contextHeader := ""
	if meetingTitle != "" || len(participants) > 0 {
		contextHeader = "TOPLANTI BAĞLAMI:\n"
		if meetingTitle != "" {
			contextHeader += fmt.Sprintf("- Başlık: %s\n", meetingTitle)
		}
		if len(participants) > 0 {
			contextHeader += fmt.Sprintf("- Katılımcılar: %s\n", participants)
		}
		contextHeader += "\n"
	}

	// Build dynamic domain terms from dictionary
	domainTermsSection := s.buildDomainTermsPrompt()
	if domainTermsSection == "" {
		domainTermsSection = "DOMAIN TERİMLERİ: (sözlük boş — genel STT düzeltmeleri uygula)\n"
	}

	// Updated Prompt for BLOCKS with context
	systemPrompt := contextHeader + `Sen uzman bir transkript redaktörüsün. Sana verilen konuşma bloklarındaki STT (Speech-to-Text) hatalarını düzelt.

GÖREVLER:
- STT hatalarını düzelt (örn: ses benzerliği nedeniyle yanlış duyulmuş kelimeler)
- Toplantı bağlamına uygun olmayan kelimeleri kontrol et (örn: MIA demo'sunda "Midjourney" absürt olur)
- Gereksiz dolgu kelimelerini (eee, ııı, şey vb.) temizle
- Terminolojik hataları düzelt (aşağıdaki DOMAIN TERİMLERİ bölümüne bak)
- Gramer ve akıcılığı iyileştir
- ASLA metni özetleme veya içeriği değiştirme
- Parantez içi ses efektlerini koru: "(gülüşmeler)", "(alkış sesleri)" vb.

` + domainTermsSection + `

ÖRNEK DÜZELTMELER:
Girdi: "eee şey bi MIA demosunu göstericez işte"
Çıktı: "MIA demo'sunu göstereceğiz."

Girdi: "o pos payment sistemini entegre edicez ya"
Çıktı: "O POS Payment sistemini entegre edeceğiz."

Girdi: "pre miting de şeyi konuşcaz hani ajandayı"
Çıktı: "Pre-meeting'de ajandayı konuşacağız."

Girdi: "tamam evet anladım şey yani rag kullanarak yapıcaz"
Çıktı: "Anladım, RAG kullanarak yapacağız."

Girdi: "Bi dakka e bilmiyom şimdi"
Çıktı: "Bir dakika, bilmiyorum şimdi."

Çıktı JSON formatında: { "items": [ {"id": "...", "corrected_text": "..."}, ... ] }`

	wrapper, usage, err := s.callOpenAICorrectionWithRetry(systemPrompt, string(inputJSON))
	if err != nil {
		return nil, correctionTokenUsage{}, err
	}

	var batchUsage correctionTokenUsage
	if usage != nil {
		batchUsage = *usage
	}

	// Map results to TranscriptChatBlock models
	var resultBlocks []models.TranscriptChatBlock

	// Create map for easy lookup
	correctedMap := make(map[string]string)
	for _, item := range wrapper.Items {
		correctedMap[item.ID] = item.CorrectedText
	}

	for _, inputItem := range inputItems {
		tempID := inputItem.ID
		aggBlock, exists := blockIDMap[tempID]
		if !exists {
			continue
		}
		correctedText := aggBlock.OriginalText
		if c, ok := correctedMap[tempID]; ok {
			if correctionDiffGuard(aggBlock.OriginalText, c) {
				correctedText = c
			} else {
				slog.Warn("Diff guard rejected correction", "block_id", tempID, "original_words", len(strings.Fields(aggBlock.OriginalText)), "corrected_words", len(strings.Fields(c)))
			}
		}

		// Collect raw segment IDs
		var rawIDs []uuid.UUID
		for _, s := range aggBlock.Segments {
			rawIDs = append(rawIDs, s.ID)
		}

		// Quality detection: noise filtering (pre + post-correction + ambient + hallucination)
		duration := aggBlock.EndTime - aggBlock.StartTime
		isNoise := detectNoise(correctedText, duration)
		if !isNoise {
			isNoise = detectPostCorrectionNoise(correctedText, duration)
		}
		if !isNoise {
			isNoise = detectAmbientSound(correctedText)
		}
		if !isNoise {
			isNoise = detectSTTHallucination(correctedText)
		}

		resultBlocks = append(resultBlocks, models.TranscriptChatBlock{
			ID:            uuid.New(),
			MeetingID:     meetingID,
			Speaker:       aggBlock.Speaker,
			Text:          correctedText,
			StartTime:     aggBlock.StartTime,
			EndTime:       aggBlock.EndTime,
			RawSegmentIDs: rawIDs,
			IsNoise:       isNoise,
		})
	}

	return resultBlocks, batchUsage, nil
}

// ==================== OpenAI API Call with Retry + Model Fallback ====================

type correctionResponseWrapper struct {
	Items []BlockCorrectionResponseItem `json:"items"`
}

// correctionTokenUsage tracks token consumption from OpenAI correction calls.
type correctionTokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// isRetryableStatus returns true for HTTP status codes that are worth retrying.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503
}

// callOpenAICorrectionWithRetry calls OpenAI with retry logic, model fallback, and circuit breaker.
// Sequence: check circuit breaker → primary model (retry up to correctionMaxRetries) → fallback model (retry).
// On success: circuit breaker resets. On failure: circuit breaker increments.
func (s *CorrectionService) callOpenAICorrectionWithRetry(systemPrompt, userContent string) (*correctionResponseWrapper, *correctionTokenUsage, error) {
	// Circuit breaker: reject immediately if open
	if s.cbIsOpen() {
		return nil, nil, fmt.Errorf("circuit breaker open: OpenAI calls paused due to repeated failures")
	}

	var lastErr error
	for attempt := 0; attempt <= correctionMaxRetries; attempt++ {
		wrapper, usage, err := s.callOpenAICorrection(correctionModel, systemPrompt, userContent)
		if err == nil {
			s.cbRecordSuccess()
			return wrapper, usage, nil
		}
		lastErr = err

		// Don't retry on non-retryable errors (e.g. 400 bad request, auth errors)
		if !isRetryableError(err) {
			slog.Error("Non-retryable error", "model", correctionModel, "error", err)
			break
		}

		// If error contains "insufficient_quota", don't retry — it won't recover
		if strings.Contains(err.Error(), "insufficient_quota") {
			slog.Error("OpenAI quota exhausted, skipping retries", "model", correctionModel)
			s.cbRecordFailure()
			return nil, nil, fmt.Errorf("all correction attempts failed: %w", lastErr)
		}

		if attempt < correctionMaxRetries {
			backoff := time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s
			slog.Warn("Retrying correction", "attempt", attempt+1, "max_retries", correctionMaxRetries, "model", correctionModel, "backoff", backoff, "error", err)
			time.Sleep(backoff)
		}
	}

	s.cbRecordFailure()
	return nil, nil, fmt.Errorf("all correction attempts failed: %w", lastErr)
}

// isRetryableError checks if an error message indicates a retryable condition.
func isRetryableError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "429") || strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") || strings.Contains(msg, "503") ||
		strings.Contains(msg, "timeout") || strings.Contains(msg, "connection")
}

// callOpenAICorrection performs a single OpenAI API call with the given model.
func (s *CorrectionService) callOpenAICorrection(model, systemPrompt, userContent string) (*correctionResponseWrapper, *correctionTokenUsage, error) {
	requestBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{"type": "json_object"},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nil, fmt.Errorf("OpenAI API error: %s (status %d): %s", resp.Status, resp.StatusCode, string(body))
	}

	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, nil, err
	}

	if len(openAIResp.Choices) == 0 {
		return nil, nil, fmt.Errorf("no choices in OpenAI response")
	}

	var wrapper correctionResponseWrapper
	if err := json.Unmarshal([]byte(openAIResp.Choices[0].Message.Content), &wrapper); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OpenAI JSON: %w", err)
	}

	usage := &correctionTokenUsage{
		InputTokens:  openAIResp.Usage.PromptTokens,
		OutputTokens: openAIResp.Usage.CompletionTokens,
	}

	return &wrapper, usage, nil
}

// ==================== Correction Diff Guard ====================

const (
	// If corrected text has fewer than 30% of the original words, GPT likely summarized it.
	diffGuardMinRatio = 0.3
	// If corrected text has more than 250% of the original words, GPT likely hallucinated.
	diffGuardMaxRatio = 2.5
	// Skip guard for very short texts (word count) — too few words to measure reliably.
	diffGuardMinWords = 4
)

// correctionDiffGuard returns true if the correction is acceptable.
// It rejects corrections where GPT drastically shortened (summarized) or expanded (hallucinated) the text.
func correctionDiffGuard(original, corrected string) bool {
	originalWords := len(strings.Fields(original))
	correctedWords := len(strings.Fields(corrected))

	// For very short originals, any reasonable correction is fine
	if originalWords < diffGuardMinWords {
		return true
	}

	ratio := float64(correctedWords) / float64(originalWords)
	return ratio >= diffGuardMinRatio && ratio <= diffGuardMaxRatio
}

// ==================== Quick Wins: Quality Detection Logic ====================

// Noise detection blacklist (Turkish filler words)
var noiseBlacklist = []string{
	"evet", "tamam", "aynen", "ok", "hmm", "aa", "ee", "ii", "uu", "şey",
	"hı", "ha", "he", "peki", "yani", "işte", "böyle", "falan", "filan",
}

// detectNoise checks if a block is noise (QW-1)
func detectNoise(text string, duration float64) bool {
	trimmed := strings.TrimSpace(strings.ToLower(text))

	// Check 0: Text contains only special characters and whitespace (screen sharing artifacts like "- - -")
	// Preserves segments that contain real words mixed with special chars (e.g. "-  Neredeydi bizim diyagramlar? -")
	onlySpecial := true
	for _, r := range trimmed {
		if r != '-' && r != '*' && r != '_' && r != '?' && r != '.' && r != ',' &&
			r != '!' && r != ' ' && r != '\t' && r != '"' && r != '\'' &&
			r != '(' && r != ')' && r != '[' && r != ']' && r != '{' && r != '}' &&
			r != '/' && r != '\\' && r != '|' && r != '+' && r != '=' && r != '#' &&
			r != '@' && r != '&' && r != '^' && r != '~' && r != ';' && r != ':' {
			onlySpecial = false
			break
		}
	}
	if onlySpecial && len(trimmed) > 0 {
		return true
	}

	// Check 1: Single word (ANY single word is noise in transcription context)
	if !strings.Contains(trimmed, " ") {
		return true
	}

	// Check 2: Very short duration (0-0.2s) - inclusive 0
	if duration >= 0 && duration <= 0.2 {
		return true
	}

	// Check 3: Only filler words (multiple words but all in blacklist)
	words := strings.Fields(trimmed)
	if len(words) > 0 && len(words) <= 3 {
		allNoise := true
		for _, word := range words {
			found := false
			for _, noise := range noiseBlacklist {
				if word == noise {
					found = true
					break
				}
			}
			if !found {
				allNoise = false
				break
			}
		}
		if allNoise {
			return true
		}
	}

	return false
}

// detectPostCorrectionNoise catches noise patterns that become visible after GPT correction
func detectPostCorrectionNoise(correctedText string, duration float64) bool {
	trimmed := strings.TrimSpace(correctedText)

	// 1. GPT returned empty or near-empty after correction (original was noise)
	if len(trimmed) == 0 || trimmed == "." || trimmed == "..." || trimmed == "-" {
		return true
	}

	// 2. Repetition detection: same word repeated 3+ times (e.g. "evet evet evet")
	words := strings.Fields(strings.ToLower(trimmed))
	if len(words) >= 3 {
		wordCounts := make(map[string]int)
		for _, w := range words {
			wordCounts[w]++
		}
		for _, count := range wordCounts {
			if count >= 3 && float64(count)/float64(len(words)) >= 0.6 {
				return true
			}
		}
	}

	// 3. Very short corrected text with short duration (GPT stripped fillers, little remains)
	if len(trimmed) < 5 && duration < 1.0 {
		return true
	}

	// 4. Text is only punctuation or symbols
	cleaned := strings.Map(func(r rune) rune {
		if r == '.' || r == ',' || r == '!' || r == '?' || r == '-' || r == ' ' || r == '"' || r == '\'' {
			return -1 // drop
		}
		return r
	}, trimmed)
	if len(strings.TrimSpace(cleaned)) == 0 {
		return true
	}

	return false
}

// Known ambient sound keywords (Turkish + English, lowercase)
var ambientSoundKeywords = []string{
	"gülüşmeler", "gülüşme", "kahkaha", "alkış", "alkış sesleri",
	"kapı", "kapı sesi", "kapı çalma sesi", "zil sesi",
	"telefon", "telefon sesi", "bildirim sesi",
	"müzik", "müzik sesi", "sessizlik", "uzun sessizlik",
	"gürültü", "arka plan gürültüsü", "parazit",
	"öksürük", "hapşırık", "nefes sesi",
	"ekran kapama sesi", "mikrofon sesi",
	"laughter", "applause", "music", "silence", "noise", "coughing",
	"inaudible", "crosstalk", "background noise",
}

// detectAmbientSound checks if text is ONLY an ambient sound marker (QW-4).
// A block is noise only if its entire meaningful content is a known ambient pattern
// like "(gülüşmeler)" or "[müzik]". Blocks with ambient markers mixed into real
// speech (e.g. "POS (Point of Sale) sistemi") are NOT noise.
func detectAmbientSound(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}

	// Strip all bracket/paren content and check if nothing meaningful remains.
	// e.g. "(gülüşmeler)" → "" → noise
	// e.g. "Evet arkadaşlar (gülüşmeler) devam edelim" → "Evet arkadaşlar  devam edelim" → not noise
	bracketPattern := regexp.MustCompile(`[\(\[\{].*?[\)\]\}]`)
	withoutBrackets := strings.TrimSpace(bracketPattern.ReplaceAllString(trimmed, ""))

	// If removing bracketed content leaves nothing, check if brackets contained ambient sounds
	if withoutBrackets == "" {
		// Extract what's inside the brackets
		innerPattern := regexp.MustCompile(`[\(\[\{](.*?)[\)\]\}]`)
		matches := innerPattern.FindAllStringSubmatch(trimmed, -1)
		for _, match := range matches {
			if len(match) > 1 {
				inner := strings.TrimSpace(strings.ToLower(match[1]))
				for _, keyword := range ambientSoundKeywords {
					if inner == keyword || strings.Contains(inner, keyword) {
						return true
					}
				}
			}
		}
		// Brackets with unknown content — not ambient sound, could be meaningful
		return false
	}

	// Meaningful text exists outside brackets — not noise
	return false
}

// Known STT hallucination patterns (YouTube/platform training data artifacts).
// ElevenLabs (and other STT engines trained on YouTube) produce these during silence.
var sttHallucinationPatterns = []string{
	"kanalımıza abone",
	"abone olmayı unutmayın",
	"beğenmeyi ve abone",
	"beğen ve abone ol",
	"kanalımızı takip",
	"like and subscribe",
	"subscribe to",
	"don't forget to subscribe",
	"thanks for watching",
	"izlediğiniz için teşekkür",
}

// detectSTTHallucination checks if text matches known STT hallucination patterns.
// These are phrases that speech-to-text engines hallucinate during silence,
// typically from YouTube training data (e.g. "Kanalımıza abone olmayı unutmayın").
func detectSTTHallucination(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, pattern := range sttHallucinationPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// recordCorrectionUsage looks up the meeting owner and records transcript correction token usage.
func (s *CorrectionService) recordCorrectionUsage(meetingID uuid.UUID, usage correctionTokenUsage) {
	meeting, err := s.meetingRepo.GetMeetingByIDSystem(context.Background(), meetingID)
	if err != nil {
		slog.Error("Failed to lookup meeting for correction usage", "meeting_id", meetingID, "error", err)
		return
	}

	mID := meetingID
	s.usageRecorder.Record(
		meeting.UserID,
		nil,
		&mID,
		"transcript_correction",
		"openai",
		correctionModel,
		usage.InputTokens,
		usage.OutputTokens,
		0,
		map[string]interface{}{
			"source": "correction_service",
		},
	)
}

