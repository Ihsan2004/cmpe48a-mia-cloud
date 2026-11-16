import os
import sys
import json

# Manual .env loader
def load_env_manual(filepath):
    try:
        with open(filepath, 'r') as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith('#'):
                    continue
                if '=' in line:
                    key, value = line.split('=', 1)
                    # Handle quotes
                    value = value.strip()
                    if (value.startswith('"') and value.endswith('"')) or (value.startswith("'") and value.endswith("'")):
                        value = value[1:-1]
                    os.environ[key.strip()] = value
        print("✅ Environment loaded from .env")
    except FileNotFoundError:
        print("⚠️ .env file not found")

# Load env vars
load_env_manual(".env")
sys.path.append(os.getcwd())

from app.services.nlp_service import NLPService

def test_phase2():
    print("🚀 Initializing NLP Service for Phase 2 Test...")
    nlp = NLPService()
    
    # Read sample transcript
    transcript_path = "../212 __ Mia _otter_ai.txt"
    try:
        with open(transcript_path, "r", encoding="utf-8") as f:
            transcript = f.read()
    except FileNotFoundError:
        print(f"❌ Could not find transcript at {transcript_path}")
        return

    # 1. Segment
    print("\n🧠 STEP 1: Running Topic Segmentation...")
    chunks = nlp.segment_transcript_by_topic(transcript)
    print(f"✅ Got {len(chunks)} chunks.")

    # 2. Analyze specific chunk (e.g., Chunk 6 which seemed technical)
    target_idx = 5 if len(chunks) > 5 else 0
    target_chunk = chunks[target_idx]
    
    print(f"\n🕵️ STEP 2: Analyzing Chunk {target_idx + 1} (Analysis Agent)...")
    print(f"Chunk Preview: {target_chunk[:100].replace(chr(10), ' ')}...")
    
    # Mock master instruction
    instruction = "Bu bir teknik planlama toplantısıdır. Kararlara ve teknik detaylara odaklan."
    
    result = nlp.analyze_topic_segment(target_chunk, instruction, target_idx)

    print("\n✅ ANALYSIS RESULT (JSON Structure):")
    print(json.dumps(result, indent=2, ensure_ascii=False))

    # Basic validations
    if not result.get("topic_title"):
        print("❌ Error: Missing topic_title")
    if not result.get("summary"):
        print("❌ Error: Missing summary")
    
    formatted_actions = result.get("actions", [])
    if formatted_actions:
        print(f"\n✅ Captured {len(formatted_actions)} Action Items:")
        for action in formatted_actions:
            print(f"  - {action}")
    else:
        print("\n⚠️ No actions found in this chunk (might be expected)")

if __name__ == "__main__":
    test_phase2()
