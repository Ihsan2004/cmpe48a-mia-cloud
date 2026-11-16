/**
 * Teams WebRTC Interceptor — browser-injected JavaScript (Faz 3 / Phase 3).
 *
 * Adapted from the Attendee project's Teams payload for Playwright integration:
 *
 *   1. RTCPeerConnection monkey-patch (Reflect.construct, same as Google Meet)
 *   2. "main-channel" data channel interception (incoming + outgoing)
 *   3. DSH (Dominant Speaker History) parsing from binary→JSON messages
 *   4. Caption message parsing (recognitionResults with NTP timestamps)
 *   5. Source request interception (virtual → physical stream mapping)
 *   6. callManager polling for participant roster (MRI → displayName)
 *   7. Periodic audio track scanning (Teams creates PCs at different times)
 *   8. CSRC hysteresis (same 1-start/4-stop pattern as Google Meet)
 *   9. Per-participant audio: audio tracks → AttributionBuffer (Faz 9)
 *
 * Data channel message formats:
 *   - DSH (array):   [{"history":[1053,2331],"type":"dsh"}]
 *                     history[0] = dominant speaker virtual stream ID
 *   - Caption (obj):  { recognitionResults: [{ userId, text, timestampAudioSent }] }
 *                     timestampAudioSent is NTP 100ns ticks
 *   - Source req (outbound obj): { type: "sr", sourceId: "1053", streamMsid: "..." }
 *
 * Must be injected EARLY via page.addInitScript() — before Teams establishes
 * WebRTC connections.
 */

export const teamsWebrtcInterceptorJs = `
(function() {
  if (window.__miaTeamsWebrtc) return;

  // ---------------------------------------------------------------
  // State — mirrors Attendee's UserManager + DominantSpeakerManager
  // ---------------------------------------------------------------
  var state = window.__miaTeamsWebrtc = {
    peerConnections: [],
    userMap: new Map(),            // mri -> { mri, displayName }
    virtualToPhysical: new Map(),  // virtualStreamId (string) -> physicalStreamMsid
    physicalToMri: new Map(),      // physicalStreamMsid -> mri
    dominantSpeaker: null,         // { virtualStreamId, mri, displayName, timestamp }
    lastCaption: null,             // { userId, displayName, text, timestamp }
    audioReceivers: new Set(),     // Set<RTCRtpReceiver>
    speakerState: new Map(),       // CSRC source -> { speaking, startCount, stopCount, lastSeen, audioLevel }
    mainChannelFound: false,
    callManagerAvailable: false,
    log: [],
  };

  function addLog(msg) {
    state.log.push('[' + new Date().toISOString() + '] ' + msg);
    if (state.log.length > 200) state.log.shift();
  }

  // ---------------------------------------------------------------
  // User Management — callManager polling
  //
  // Teams exposes window.callManager after call setup. We poll it
  // to get the participant roster (MRI → displayName) and stream
  // mappings (endpoint.mediaStreams → virtual/physical IDs).
  // ---------------------------------------------------------------
  function pollCallManager() {
    try {
      var cm = window.callManager;
      if (!cm) return;
      state.callManagerAvailable = true;

      var participants = cm.participantsManager && cm.participantsManager.participants;
      if (!participants) return;

      var added = 0;
      participants.forEach(function(p) {
        var mri = p.mri || p.id;
        var name = p.displayName || p.name;
        if (mri && name) {
          if (!state.userMap.has(mri)) added++;
          state.userMap.set(mri, { mri: mri, displayName: name });
        }
        // Map endpoint streams to MRI
        if (p.endpoints) {
          p.endpoints.forEach(function(ep) {
            if (ep.mediaStreams) {
              ep.mediaStreams.forEach(function(ms) {
                if (ms.streamId) {
                  state.physicalToMri.set(ms.streamId, mri);
                }
                if (ms.sourceId) {
                  state.virtualToPhysical.set(String(ms.sourceId), ms.streamId || '');
                }
              });
            }
          });
        }
      });
      if (added > 0) addLog('callManager: +' + added + ' users, total=' + state.userMap.size);
    } catch(e) {
      // callManager not available yet — expected before call setup
    }
  }

  // Poll callManager periodically (initial rapid + steady state)
  setTimeout(pollCallManager, 1000);
  setTimeout(pollCallManager, 2000);
  setTimeout(pollCallManager, 5000);
  setInterval(pollCallManager, 3000);

  // ---------------------------------------------------------------
  // Data Channel Message Parsing — binary → JSON
  //
  // Teams main-channel sends binary data with a JSON payload.
  // Binary header is followed by first '[' or '{' byte.
  // ---------------------------------------------------------------
  function parseMainChannelMessage(data) {
    try {
      if (typeof data === 'string') {
        return JSON.parse(data);
      }

      var buf;
      if (data instanceof ArrayBuffer) {
        buf = new Uint8Array(data);
      } else if (data instanceof Uint8Array) {
        buf = data;
      } else {
        return null;
      }

      // Find first JSON boundary ([ or {)
      var jsonStart = -1;
      for (var i = 0; i < buf.length && i < 100; i++) {
        if (buf[i] === 0x5B || buf[i] === 0x7B) { // [ or {
          jsonStart = i;
          break;
        }
      }

      if (jsonStart < 0) return null;
      var jsonStr = new TextDecoder('utf-8').decode(buf.subarray(jsonStart));
      return JSON.parse(jsonStr);
    } catch(e) {
      return null;
    }
  }

  // ---------------------------------------------------------------
  // Main Channel Inbound Message Handler
  // ---------------------------------------------------------------
  function handleMainChannelMessage(event) {
    var parsed = parseMainChannelMessage(event.data);
    if (!parsed) return;

    // DSH message (array): [{"history":[1053,2331],"type":"dsh"}]
    if (Array.isArray(parsed)) {
      for (var i = 0; i < parsed.length; i++) {
        var msg = parsed[i];
        if (msg.type === 'dsh' && msg.history && msg.history.length > 0) {
          handleDshMessage(msg);
        }
      }
      return;
    }

    // Caption message (object): { recognitionResults: [...] }
    if (parsed.recognitionResults) {
      handleCaptionMessage(parsed);
      return;
    }
  }

  // ---------------------------------------------------------------
  // Outbound Message Interception (source request)
  // ---------------------------------------------------------------
  function handleOutboundMessage(data) {
    try {
      var parsed;
      if (typeof data === 'string') {
        parsed = JSON.parse(data);
      } else {
        var buf = data instanceof ArrayBuffer ? new Uint8Array(data) : data;
        for (var i = 0; i < buf.length && i < 50; i++) {
          if (buf[i] === 0x7B) { // {
            parsed = JSON.parse(new TextDecoder('utf-8').decode(buf.subarray(i)));
            break;
          }
        }
      }
      if (!parsed) return;

      // Source request: { type: "sr", sourceId: "1053", streamMsid: "..." }
      if (parsed.type === 'sr' && parsed.sourceId && parsed.streamMsid) {
        state.virtualToPhysical.set(String(parsed.sourceId), parsed.streamMsid);
        addLog('Source mapping: virtual=' + parsed.sourceId + ' -> physical=' + parsed.streamMsid);
      }
    } catch(e) {
      // parse error — ignore
    }
  }

  // ---------------------------------------------------------------
  // DSH (Dominant Speaker History) Handler
  //
  // Attendee's DominantSpeakerManager: history[0] is the current
  // dominant speaker's virtual stream ID. Resolve through:
  //   virtual → physical (virtualToPhysical map, from sr messages)
  //   physical → mri (physicalToMri map, from callManager)
  //   mri → displayName (userMap, from callManager)
  // ---------------------------------------------------------------
  function handleDshMessage(msg) {
    var dominantVirtualId = String(msg.history[0]);

    // Resolve: virtual → physical → MRI → user
    var physicalId = state.virtualToPhysical.get(dominantVirtualId);
    var mri = physicalId ? state.physicalToMri.get(physicalId) : null;
    var user = mri ? state.userMap.get(mri) : null;

    state.dominantSpeaker = {
      virtualStreamId: dominantVirtualId,
      mri: mri || null,
      displayName: user ? user.displayName : null,
      timestamp: Date.now(),
    };

    // Per-participant audio: always feed attribution buffer from DSH (Faz 9)
    // Even when user resolution fails, use virtualStreamId as fallback identifier
    // so audio frames aren't silently dropped during callManager startup lag.
    if (window.__miaAttributionBuffer) {
      var speakerId = mri || dominantVirtualId;
      var speakerName = user ? user.displayName : ('Participant-' + dominantVirtualId);
      window.__miaAttributionBuffer.updateSpeaker(speakerId, speakerName, true, Date.now());
    }

    if (user) {
      addLog('DSH dominant: ' + user.displayName + ' (v=' + dominantVirtualId + ')');
    } else {
      addLog('DSH dominant: unresolved (v=' + dominantVirtualId + ' p=' + (physicalId || '?') + ') — attribution uses fallback ID');
    }
  }

  // ---------------------------------------------------------------
  // Caption Message Handler
  //
  // NTP timestamp conversion:
  //   timestampAudioSent is 100ns ticks from NTP epoch (1900-01-01)
  //   Unix ms = (ntpTicks / 10_000_000 - 2_208_988_800) * 1000
  // ---------------------------------------------------------------
  var NTP_EPOCH_OFFSET = 2208988800; // seconds between NTP (1900) and Unix (1970) epochs

  function handleCaptionMessage(msg) {
    if (!msg.recognitionResults || msg.recognitionResults.length === 0) return;

    for (var i = 0; i < msg.recognitionResults.length; i++) {
      var result = msg.recognitionResults[i];
      var userId = result.userId;
      var text = result.text || '';

      // NTP → Unix ms
      var timestamp = Date.now();
      if (result.timestampAudioSent) {
        var ntpSeconds = result.timestampAudioSent / 10000000; // 100ns ticks → seconds
        timestamp = (ntpSeconds - NTP_EPOCH_OFFSET) * 1000;
      }

      // Resolve userId (MRI) → display name
      var user = state.userMap.get(userId);
      var displayName = user ? user.displayName : null;

      if (displayName && text.trim()) {
        state.lastCaption = {
          userId: userId,
          displayName: displayName,
          text: text.trim(),
          timestamp: Date.now(), // use wall clock for recency check
        };
        addLog('Caption: ' + displayName + ' -> "' + text.trim().substring(0, 40) + '"');

        // Feed attribution buffer from caption signal (P1 fix: multiple attribution sources)
        // Caption = person is speaking. Debounced stop after 3s of no caption from this user.
        if (window.__miaAttributionBuffer) {
          var captionSpeakerId = userId || ('caption:' + displayName);
          window.__miaAttributionBuffer.updateSpeaker(captionSpeakerId, displayName, true, Date.now());

          // Schedule stop signal — cleared if another caption arrives before timeout
          var stopKey = '__miaCaptionStop_' + captionSpeakerId;
          if (window[stopKey]) clearTimeout(window[stopKey]);
          window[stopKey] = setTimeout(function() {
            if (window.__miaAttributionBuffer) {
              window.__miaAttributionBuffer.updateSpeaker(captionSpeakerId, displayName, false, Date.now());
            }
            delete window[stopKey];
          }, 3000);
        }
      }
    }
  }

  // ---------------------------------------------------------------
  // CSRC Hysteresis — same pattern as Google Meet (Faz 6.5)
  //
  // 1 sample to start speaking, 4 samples to stop.
  // Teams may or may not expose CSRC data — this is a bonus signal.
  // ---------------------------------------------------------------
  var HYSTERESIS_START = 1;
  var HYSTERESIS_STOP  = 4;
  var SPEAKER_TIMEOUT  = 10000;

  var origGetCS = RTCRtpReceiver.prototype.getContributingSources;
  RTCRtpReceiver.prototype.getContributingSources = function() {
    var result = origGetCS.apply(this, arguments);
    if (result && result.length > 0) {
      updateSpeakerHysteresis(result);
    }
    return result;
  };

  function updateSpeakerHysteresis(contributingSources) {
    var now = Date.now();
    for (var i = 0; i < contributingSources.length; i++) {
      var src = contributingSources[i];
      var sid = src.source.toString();
      var isSpeaking = typeof src.audioLevel === 'number' && src.audioLevel >= 0.01;

      var entry = state.speakerState.get(sid);
      if (!entry) {
        entry = { speaking: false, startCount: 0, stopCount: 0, lastSeen: now, audioLevel: 0 };
        state.speakerState.set(sid, entry);
      }

      entry.lastSeen = now;
      entry.audioLevel = src.audioLevel || 0;

      if (isSpeaking) {
        entry.startCount++;
        entry.stopCount = 0;
        if (!entry.speaking && entry.startCount >= HYSTERESIS_START) {
          entry.speaking = true;
        }
      } else {
        entry.stopCount++;
        entry.startCount = 0;
        if (entry.speaking && entry.stopCount >= HYSTERESIS_STOP) {
          entry.speaking = false;
        }
      }
    }

    // Prune stale entries
    state.speakerState.forEach(function(v, k) {
      if (now - v.lastSeen > SPEAKER_TIMEOUT) state.speakerState.delete(k);
    });
  }

  // ---------------------------------------------------------------
  // RTCPeerConnection Patch — Reflect.construct (Attendee pattern)
  // ---------------------------------------------------------------
  var OrigPC = window.RTCPeerConnection;

  window.RTCPeerConnection = function() {
    var pc = Reflect.construct(OrigPC, Array.prototype.slice.call(arguments));
    state.peerConnections.push(pc);
    addLog('RTCPeerConnection created (total: ' + state.peerConnections.length + ')');

    // Intercept incoming audio tracks
    pc.addEventListener('track', function(event) {
      try {
        if (event.receiver && event.track && event.track.kind === 'audio') {
          state.audioReceivers.add(event.receiver);
          addLog('Teams audio track registered (total: ' + state.audioReceivers.size + ')');

          // Warm CSRC cache
          try { event.receiver.getContributingSources(); } catch(e) {}

          // Per-participant audio: connect track to attribution buffer (Faz 9)
          if (window.__miaAttributionBuffer) {
            window.__miaAttributionBuffer.connectAudioTrack(event.track);
            addLog('Audio track connected to attribution buffer');
          }
        }
      } catch(e) {}
    });

    // Intercept incoming remote data channels (main-channel)
    pc.addEventListener('datachannel', function(event) {
      var channel = event.channel;
      channel.binaryType = 'arraybuffer';
      addLog('Teams datachannel event: ' + channel.label);

      if (channel.label === 'main-channel' || channel.label.indexOf('main') !== -1) {
        state.mainChannelFound = true;
        addLog('Teams main-channel intercepted (remote)');
        channel.addEventListener('message', handleMainChannelMessage);
      }
    });

    // Intercept outgoing data channels
    var origCreateDC = pc.createDataChannel.bind(pc);
    pc.createDataChannel = function(label, options) {
      var dc = origCreateDC(label, options);
      dc.binaryType = 'arraybuffer';
      addLog('Teams createDataChannel: ' + label);

      if (label === 'main-channel' || label.indexOf('main') !== -1) {
        state.mainChannelFound = true;
        addLog('Teams main-channel intercepted (local)');

        // Listen for incoming messages
        dc.addEventListener('message', handleMainChannelMessage);

        // Intercept outbound send() for source request messages
        var origSend = dc.send.bind(dc);
        dc.send = function(data) {
          handleOutboundMessage(data);
          return origSend(data);
        };
      }
      return dc;
    };

    return pc;
  };

  window.RTCPeerConnection.prototype = OrigPC.prototype;
  if (OrigPC.generateCertificate) {
    window.RTCPeerConnection.generateCertificate = OrigPC.generateCertificate;
  }

  // ---------------------------------------------------------------
  // Periodic Audio Track Scanning
  //
  // Teams creates multiple RTCPeerConnections at different times.
  // We scan periodically to catch any audio tracks not captured
  // by the ontrack handler (e.g. late-joining participants).
  // ---------------------------------------------------------------
  function scanForNewAudioTracks() {
    var newTracks = 0;
    state.peerConnections.forEach(function(pc) {
      if (pc.connectionState === 'closed') return;
      try {
        var receivers = pc.getReceivers();
        for (var r = 0; r < receivers.length; r++) {
          var recv = receivers[r];
          if (recv.track && recv.track.kind === 'audio' && recv.track.readyState !== 'ended') {
            if (!state.audioReceivers.has(recv)) {
              state.audioReceivers.add(recv);
              newTracks++;

              // Connect to attribution buffer
              if (window.__miaAttributionBuffer) {
                window.__miaAttributionBuffer.connectAudioTrack(recv.track);
              }
            }
          }
        }
      } catch(e) {}
    });
    if (newTracks > 0) {
      addLog('Periodic scan: +' + newTracks + ' new audio tracks (total: ' + state.audioReceivers.size + ')');
    }
  }

  // Scan schedule: 2s, 5s, 10s initial burst, then every 30s steady state
  setTimeout(scanForNewAudioTracks, 2000);
  setTimeout(scanForNewAudioTracks, 5000);
  setTimeout(scanForNewAudioTracks, 10000);
  setInterval(scanForNewAudioTracks, 30000);

  // ---------------------------------------------------------------
  // Speaker Detection — called by microsoftTeams detector (Priority 0)
  //
  // Priority chain:
  //   0. DSH dominant speaker (most reliable, from data channel)
  //   1. Caption author (from data channel recognitionResults)
  //   2. CSRC audioLevel (if Teams exposes it — bonus signal)
  // ---------------------------------------------------------------
  window.__miaTeamsWebrtcDetectSpeaker = function() {
    // Prune closed connections
    state.peerConnections = state.peerConnections.filter(function(pc) {
      return pc.connectionState !== 'closed';
    });

    // Prune ended receivers
    state.audioReceivers.forEach(function(r) {
      if (!r.track || r.track.readyState === 'ended') {
        state.audioReceivers.delete(r);
      }
    });

    var now = Date.now();

    // Priority 0: DSH (valid within 10s)
    if (state.dominantSpeaker && state.dominantSpeaker.displayName) {
      if (now - state.dominantSpeaker.timestamp < 10000) {
        return { name: state.dominantSpeaker.displayName, source: 'teams-dsh' };
      }
    }

    // Priority 1: Caption author (valid within 5s)
    if (state.lastCaption && state.lastCaption.displayName) {
      if (now - state.lastCaption.timestamp < 5000) {
        return { name: state.lastCaption.displayName, source: 'teams-caption-webrtc' };
      }
    }

    // Priority 2: CSRC-based detection (Teams may not always expose CSRC)
    var bestLevel = 0;
    var bestSpeaker = null;
    state.speakerState.forEach(function(entry, sid) {
      if (entry.speaking && entry.audioLevel > bestLevel) {
        bestLevel = entry.audioLevel;
        bestSpeaker = sid; // raw CSRC source — hard to resolve to user for Teams
      }
    });

    // CSRC source resolution is limited for Teams (no protobuf device map like Google Meet).
    // We can try callManager's stream mappings as a heuristic.
    if (bestSpeaker !== null) {
      // Try to find a user through physical stream → MRI mapping
      var physicalId = state.virtualToPhysical.get(bestSpeaker);
      var mri = physicalId ? state.physicalToMri.get(physicalId) : null;
      var user = mri ? state.userMap.get(mri) : null;
      if (user) {
        return { name: user.displayName, source: 'teams-csrc' };
      }
    }

    return null;
  };

  // ---------------------------------------------------------------
  // DSH Speaker Stop — when a new DSH message arrives, the previous
  // dominant speaker should get a "stop" signal for attribution.
  // We intercept DSH changes here (previous speaker ends, new starts).
  // ---------------------------------------------------------------
  var previousDshSpeaker = null;

  var origHandleDsh = handleDshMessage;
  handleDshMessage = function(msg) {
    var prevSpeaker = state.dominantSpeaker;

    // Call original handler (updates state.dominantSpeaker)
    origHandleDsh(msg);

    // If speaker changed, send stop for previous speaker
    if (window.__miaAttributionBuffer && prevSpeaker && prevSpeaker.displayName) {
      var newSpeaker = state.dominantSpeaker;
      if (!newSpeaker || newSpeaker.virtualStreamId !== prevSpeaker.virtualStreamId) {
        window.__miaAttributionBuffer.updateSpeaker(
          prevSpeaker.mri || prevSpeaker.virtualStreamId,
          prevSpeaker.displayName,
          false,
          Date.now()
        );
      }
    }
  };

  // ---------------------------------------------------------------
  // Diagnostic — expose state for debugging
  // ---------------------------------------------------------------
  window.__miaTeamsWebrtcDiag = function() {
    var users = [];
    state.userMap.forEach(function(v, k) {
      users.push(k.substring(0, 40) + ' -> ' + v.displayName);
    });

    var mappings = [];
    state.virtualToPhysical.forEach(function(v, k) {
      mappings.push('v:' + k + ' -> p:' + v.substring(0, 40));
    });

    var physMappings = [];
    state.physicalToMri.forEach(function(v, k) {
      physMappings.push('p:' + k.substring(0, 40) + ' -> mri:' + v.substring(0, 40));
    });

    var speakerStates = [];
    state.speakerState.forEach(function(v, k) {
      speakerStates.push({
        source: k,
        speaking: v.speaking,
        audioLevel: v.audioLevel,
        ageSec: ((Date.now() - v.lastSeen) / 1000).toFixed(1),
      });
    });

    return {
      peerConnections: state.peerConnections.length,
      mainChannelFound: state.mainChannelFound,
      callManagerAvailable: state.callManagerAvailable,
      userMapSize: state.userMap.size,
      userMapSample: users.slice(0, 10),
      virtualToPhysicalSize: state.virtualToPhysical.size,
      virtualMappingSample: mappings.slice(0, 10),
      physicalToMriSize: state.physicalToMri.size,
      physicalMappingSample: physMappings.slice(0, 5),
      dominantSpeaker: state.dominantSpeaker,
      lastCaption: state.lastCaption ? {
        displayName: state.lastCaption.displayName,
        text: state.lastCaption.text.substring(0, 40),
        ageSec: ((Date.now() - state.lastCaption.timestamp) / 1000).toFixed(1),
      } : null,
      audioReceivers: state.audioReceivers.size,
      csrcSpeakerStates: speakerStates,
      recentLog: state.log.slice(-20),
    };
  };

  console.log('[MiaTeamsWebRTC] Interceptor installed (DSH + caption + CSRC + periodic scan)');
})();
`;
