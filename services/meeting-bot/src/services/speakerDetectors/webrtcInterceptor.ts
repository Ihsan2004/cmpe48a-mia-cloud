/**
 * WebRTC Speaker Detection — browser-injected JavaScript (Faz 6.5).
 *
 * Fully aligned with Attendee (github.com/attendee-labs/attendee):
 *
 *   1. Schema-based protobuf decoder (same field numbers as Attendee's protobufjs schema)
 *   2. RTCPeerConnection patch via Reflect.construct (Attendee pattern)
 *   3. RTCRtpReceiver.prototype.getContributingSources prototype patch
 *   4. "collections" data channel: pako.inflate equivalent via DecompressionStream
 *   5. Fetch intercept for SyncMeetingSpaceCollections (initial user list)
 *   6. CSRC → getUserByStreamId → deviceOutputMap → allUsersMap → displayName
 *
 * Attendee's protobuf schema for DeviceOutputInfoList:
 *   field 2 (varint) = deviceOutputType
 *   field 4 (string) = streamId
 *   field 6 (string) = deviceId
 *   field 10 (message) = deviceOutputStatus
 *
 * Attendee's UserInfoList (per-participant):
 *   field 1 (string) = deviceId
 *   field 2 (string) = fullName
 *   field 4 (varint) = status (1=in meeting, 6=not in meeting)
 *   field 7 (string) = isCurrentUserString (presence = bot self)
 *   field 21 (string) = parentDeviceId (presence = screenshare device)
 *   field 29 (string) = displayName
 *   field 34 (varint) = isHost
 *
 * UserInfoListWrapper.userInfoList is field 2 (NOT field 1)
 *
 * Must be injected EARLY via page.addInitScript() — before Google Meet
 * establishes WebRTC connections.
 */

export const webrtcInterceptorJs = `
(function() {
  if (window.__miaWebrtc) return;

  // ---------------------------------------------------------------
  // State — mirrors Attendee's UserManager + DeviceOutputMap
  // ---------------------------------------------------------------
  var state = window.__miaWebrtc = {
    peerConnections: [],
    allUsersMap: new Map(),       // deviceId -> { deviceId, fullName, displayName }
    deviceOutputMap: new Map(),   // "deviceId-type" -> { deviceId, outputType, streamId, disabled }
    receiverMap: new Map(),       // RTCRtpReceiver -> [{ source, audioLevel, timestamp }]
    syncSourceMap: new Map(),     // RTCRtpReceiver -> [{ source, audioLevel, timestamp }] (SSRC)
    audioReceivers: new Set(),    // Set<RTCRtpReceiver> — registered via track events (Attendee pattern)
    speakerState: new Map(),      // streamId -> { speaking, samples, lastSeen }
    collectionsChannelFound: false,
    log: [],
  };

  function addLog(msg) {
    state.log.push('[' + new Date().toISOString() + '] ' + msg);
    if (state.log.length > 200) state.log.shift();
  }

  // ---------------------------------------------------------------
  // Minimal Protobuf Reader (replaces protobufjs dependency)
  //
  // Implements the same Reader interface Attendee uses:
  //   reader.uint32() / reader.varint()  — read varint
  //   reader.string()                    — read length-delimited UTF-8
  //   reader.skipType(wireType)          — skip unknown field
  // ---------------------------------------------------------------
  function PbReader(buf) {
    this.buf = (buf instanceof Uint8Array) ? buf : new Uint8Array(buf);
    this.pos = 0;
    this.len = this.buf.length;
  }

  PbReader.prototype.varint = function() {
    var value = 0;
    var shift = 0;
    while (this.pos < this.len) {
      var b = this.buf[this.pos++];
      value += (b & 0x7F) * Math.pow(2, shift);
      if ((b & 0x80) === 0) return value;
      shift += 7;
      if (shift > 63) return value;
    }
    return value;
  };

  PbReader.prototype.uint32 = PbReader.prototype.varint;

  PbReader.prototype.string = function() {
    var len = this.varint();
    if (len <= 0 || this.pos + len > this.len) {
      this.pos = Math.min(this.pos + Math.max(len, 0), this.len);
      return '';
    }
    var bytes = this.buf.slice(this.pos, this.pos + len);
    this.pos += len;
    try {
      return new TextDecoder('utf-8').decode(bytes);
    } catch(e) {
      return '';
    }
  };

  PbReader.prototype.skipType = function(wireType) {
    switch (wireType) {
      case 0:
        while (this.pos < this.len && (this.buf[this.pos++] & 0x80)) {}
        break;
      case 1:
        this.pos = Math.min(this.pos + 8, this.len);
        break;
      case 2:
        var len = this.varint();
        this.pos = Math.min(this.pos + len, this.len);
        break;
      case 5:
        this.pos = Math.min(this.pos + 4, this.len);
        break;
    }
  };

  // ---------------------------------------------------------------
  // Schema Definitions — Attendee-exact field numbers
  //
  // Source: attendee-labs/attendee google_meet_chromedriver_payload.js
  // lines 1394-1526 (messageTypes array)
  //
  // Nesting path to DeviceOutput:
  //   CollectionEvent (f1) -> CollectionEventBody (f2)
  //     -> UserInfoListWrapperAndChatWrapperWrapper
  //       -> (f3) DeviceInfoWrapper -> (f2 repeated) DeviceOutputInfoList
  //       -> (f13) UserInfoListWrapperAndChatWrapper -> (f1) UserInfoListWrapper -> (f2 repeated) UserInfoList
  // ---------------------------------------------------------------
  var schema = {
    CollectionEvent: {
      fields: [
        { name: 'body', fn: 1, type: 'message', mt: 'CollectionEventBody' }
      ]
    },
    CollectionEventBody: {
      fields: [
        { name: 'wrapper', fn: 2, type: 'message', mt: 'UIWAndChatWW' }
      ]
    },
    UIWAndChatWW: {
      fields: [
        { name: 'deviceInfoWrapper', fn: 3, type: 'message', mt: 'DeviceInfoWrapper' },
        { name: 'userListWC', fn: 13, type: 'message', mt: 'UserInfoListWrapperAndChatWrapper' }
      ]
    },
    DeviceInfoWrapper: {
      fields: [
        { name: 'deviceOutputInfoList', fn: 2, type: 'message', mt: 'DeviceOutputInfoList', repeated: true }
      ]
    },
    DeviceOutputInfoList: {
      fields: [
        { name: 'deviceOutputType', fn: 2, type: 'varint' },
        { name: 'streamId', fn: 4, type: 'string' },
        { name: 'deviceId', fn: 6, type: 'string' },
        { name: 'deviceOutputStatus', fn: 10, type: 'message', mt: 'DeviceOutputStatus' }
      ]
    },
    DeviceOutputStatus: {
      fields: [
        { name: 'disabled', fn: 1, type: 'varint' }
      ]
    },
    UserInfoListWrapperAndChatWrapper: {
      fields: [
        { name: 'userInfoListWrapper', fn: 1, type: 'message', mt: 'UserInfoListWrapper' }
      ]
    },
    UserInfoListWrapper: {
      fields: [
        { name: 'userInfoList', fn: 2, type: 'message', mt: 'UserInfoList', repeated: true }
      ]
    },
    // Attendee calls this "UserInfoList" — each entry is one participant
    UserInfoList: {
      fields: [
        { name: 'deviceId', fn: 1, type: 'string' },
        { name: 'fullName', fn: 2, type: 'string' },
        { name: 'status', fn: 4, type: 'varint' },
        { name: 'isCurrentUserString', fn: 7, type: 'string' },
        { name: 'parentDeviceId', fn: 21, type: 'string' },
        { name: 'displayName', fn: 29, type: 'string' },
        { name: 'isHost', fn: 34, type: 'varint' }
      ]
    },
    UserInfoListResponse: {
      fields: [
        { name: 'userInfoListWrapperWrapper', fn: 2, type: 'message', mt: 'FetchUILWW' }
      ]
    },
    FetchUILWW: {
      fields: [
        { name: 'userInfoListWrapper', fn: 2, type: 'message', mt: 'UserInfoListWrapper' }
      ]
    }
  };

  // ---------------------------------------------------------------
  // Schema-based Decoder Factory
  //
  // Mirrors Attendee's createMessageDecoder() — uses field numbers
  // from schema to extract typed values. Unknown fields are skipped.
  // ---------------------------------------------------------------
  var decoders = {};

  function createDecoder(msgType) {
    var fieldList = msgType.fields;
    return function decode(reader, length) {
      if (!(reader instanceof PbReader)) {
        reader = new PbReader(reader);
      }
      var end = (length !== undefined) ? reader.pos + length : reader.len;
      var msg = {};

      while (reader.pos < end && reader.pos < reader.len) {
        var tag;
        try { tag = reader.varint(); } catch(e) { break; }
        if (tag === 0) break;

        var fieldNum = tag >>> 3;
        var wireType = tag & 7;

        if (fieldNum === 0 || fieldNum > 536870911) break;

        var field = null;
        for (var i = 0; i < fieldList.length; i++) {
          if (fieldList[i].fn === fieldNum) {
            field = fieldList[i];
            break;
          }
        }

        if (!field) {
          try { reader.skipType(wireType); } catch(e) { break; }
          continue;
        }

        var value;
        try {
          switch (field.type) {
            case 'string':
              value = reader.string();
              break;
            case 'varint':
              value = reader.varint();
              break;
            case 'message':
              var msgLen = reader.varint();
              if (msgLen < 0 || reader.pos + msgLen > reader.len) { reader.pos = end; break; }
              value = decoders[field.mt](reader, msgLen);
              break;
            default:
              reader.skipType(wireType);
              continue;
          }
        } catch(e) {
          break;
        }

        if (value === undefined) continue;

        if (field.repeated) {
          if (!msg[field.name]) msg[field.name] = [];
          msg[field.name].push(value);
        } else {
          msg[field.name] = value;
        }
      }
      return msg;
    };
  }

  for (var typeName in schema) {
    decoders[typeName] = createDecoder(schema[typeName]);
  }

  // ---------------------------------------------------------------
  // User + DeviceOutput Management (Attendee's UserManager)
  // ---------------------------------------------------------------
  function processUserInfoList(userInfoList) {
    if (!userInfoList) return;
    var added = 0;
    for (var i = 0; i < userInfoList.length; i++) {
      var u = userInfoList[i];
      // Skip screenshare devices (Attendee: parentDeviceId = screenshare)
      if (u.parentDeviceId) continue;
      // Attendee stores both: displayName (field 29) preferred, fullName (field 2) fallback
      var name = u.displayName || u.fullName;
      if (u.deviceId && name) {
        state.allUsersMap.set(u.deviceId, {
          deviceId: u.deviceId,
          fullName: u.fullName || '',
          displayName: name,
          isCurrentUser: !!u.isCurrentUserString,
        });
        added++;
      }
    }
    if (added > 0) addLog('Users updated: +' + added + ' total=' + state.allUsersMap.size);
  }

  // Attendee's updateDeviceOutputs — key is "deviceId-outputType"
  function processDeviceOutputs(deviceOutputInfoList) {
    if (!deviceOutputInfoList) return;
    var added = 0;
    for (var i = 0; i < deviceOutputInfoList.length; i++) {
      var output = deviceOutputInfoList[i];
      if (output.deviceId && output.streamId) {
        var key = output.deviceId + '-' + (output.deviceOutputType || 0);
        state.deviceOutputMap.set(key, {
          deviceId: output.deviceId,
          outputType: output.deviceOutputType || 0,
          streamId: output.streamId,
          disabled: (output.deviceOutputStatus && output.deviceOutputStatus.disabled) ? 1 : 0
        });
        added++;
      }
    }
    if (added > 0) addLog('DeviceOutputs updated: +' + added + ' total=' + state.deviceOutputMap.size);
  }

  // Attendee's getUserByStreamId — iterate deviceOutputMap for matching streamId
  function getUserByStreamId(streamId) {
    var iter = state.deviceOutputMap.values();
    var entry = iter.next();
    while (!entry.done) {
      if (entry.value.streamId === streamId) {
        return state.allUsersMap.get(entry.value.deviceId) || null;
      }
      entry = iter.next();
    }
    return null;
  }

  // ---------------------------------------------------------------
  // Async inflate via DecompressionStream (equivalent to pako.inflate)
  // ---------------------------------------------------------------
  async function inflate(compressed) {
    var formats = ['deflate', 'gzip', 'deflate-raw'];
    for (var f = 0; f < formats.length; f++) {
      try {
        var ds = new DecompressionStream(formats[f]);
        var writer = ds.writable.getWriter();
        writer.write(compressed);
        writer.close();
        var reader = ds.readable.getReader();
        var chunks = [];
        while (true) {
          var result = await reader.read();
          if (result.done) break;
          chunks.push(result.value);
        }
        var totalLen = 0;
        for (var i = 0; i < chunks.length; i++) totalLen += chunks[i].length;
        var out = new Uint8Array(totalLen);
        var offset = 0;
        for (var i = 0; i < chunks.length; i++) {
          out.set(chunks[i], offset);
          offset += chunks[i].length;
        }
        return out;
      } catch(e) {}
    }
    return null;
  }

  // ---------------------------------------------------------------
  // Collections Data Channel Handler — decompress + schema decode
  // ---------------------------------------------------------------
  async function handleCollectionEvent(messageEvent) {
    try {
      var data = messageEvent.data;
      var buf;
      if (data instanceof ArrayBuffer) {
        buf = new Uint8Array(data);
      } else if (data instanceof Uint8Array) {
        buf = data;
      } else {
        return;
      }
      if (buf.length < 4) return;

      var inflated = await inflate(buf);
      if (!inflated || inflated.length === 0) {
        inflated = buf;
      }

      var collectionEvent = decoders.CollectionEvent(inflated);

      // Extract DeviceOutputInfoList (Attendee path: body.wrapper.deviceInfoWrapper.deviceOutputInfoList)
      var deviceOutputInfoList = collectionEvent.body &&
        collectionEvent.body.wrapper &&
        collectionEvent.body.wrapper.deviceInfoWrapper &&
        collectionEvent.body.wrapper.deviceInfoWrapper.deviceOutputInfoList;

      if (deviceOutputInfoList && deviceOutputInfoList.length > 0) {
        processDeviceOutputs(deviceOutputInfoList);
      }

      // Extract UserInfoList (Attendee path: body.wrapper.userListWC.userInfoListWrapper.userInfoList)
      var userInfoListWrapper = collectionEvent.body &&
        collectionEvent.body.wrapper &&
        collectionEvent.body.wrapper.userListWC &&
        collectionEvent.body.wrapper.userListWC.userInfoListWrapper;

      if (userInfoListWrapper && userInfoListWrapper.userInfoList) {
        processUserInfoList(userInfoListWrapper.userInfoList);
      }
    } catch(e) {
      addLog('handleCollectionEvent error: ' + ((e && e.message) || String(e)));
    }
  }

  // ---------------------------------------------------------------
  // Fetch Intercept — SyncMeetingSpaceCollections (initial user list)
  //
  // Attendee intercepts this endpoint to get the initial participant
  // list before data channel messages arrive.
  // ---------------------------------------------------------------
  var origFetch = window.fetch;
  window.fetch = async function() {
    var response = await origFetch.apply(this, arguments);
    try {
      if (response.url && response.url.indexOf('SyncMeetingSpaceCollections') !== -1) {
        var cloned = response.clone();
        cloned.text().then(function(text) {
          try {
            var binaryStr = atob(text);
            var bytes = new Uint8Array(binaryStr.length);
            for (var i = 0; i < binaryStr.length; i++) {
              bytes[i] = binaryStr.charCodeAt(i);
            }
            var decoded = decoders.UserInfoListResponse(bytes);
            var userInfoList = decoded.userInfoListWrapperWrapper &&
              decoded.userInfoListWrapperWrapper.userInfoListWrapper &&
              decoded.userInfoListWrapperWrapper.userInfoListWrapper.userInfoList;
            if (userInfoList && userInfoList.length > 0) {
              processUserInfoList(userInfoList);
              addLog('Fetch intercept: ' + userInfoList.length + ' users from SyncMeetingSpaceCollections');
            }
          } catch(e) {
            addLog('Fetch decode error: ' + ((e && e.message) || String(e)));
          }
        }).catch(function() {});
      }
    } catch(e) {}
    return response;
  };

  // ---------------------------------------------------------------
  // RTCRtpReceiver.prototype.getContributingSources Patch
  //
  // Attendee's RTCRtpReceiverInterceptor — patches at prototype level
  // to cache CSRC data from every call made by Google Meet's own code.
  // ---------------------------------------------------------------
  var origGetCS = RTCRtpReceiver.prototype.getContributingSources;
  RTCRtpReceiver.prototype.getContributingSources = function() {
    var result = origGetCS.apply(this, arguments);
    if (result && result.length > 0) {
      state.receiverMap.set(this, result);
      // Feed per-speaker hysteresis (Attendee's ParticipantSpeechStartStopManager)
      updateSpeakerHysteresis(result);
    }
    return result;
  };

  // ---------------------------------------------------------------
  // RTCRtpReceiver.prototype.getSynchronizationSources Patch
  //
  // SSRC tracking — even when CSRC is empty, SSRC audioLevel can
  // indicate which virtual stream has active audio. Combined with
  // deviceOutputMap we can still identify speakers.
  // ---------------------------------------------------------------
  var origGetSS = RTCRtpReceiver.prototype.getSynchronizationSources;
  RTCRtpReceiver.prototype.getSynchronizationSources = function() {
    var result = origGetSS.apply(this, arguments);
    if (result && result.length > 0) {
      state.syncSourceMap.set(this, result);
    }
    return result;
  };

  // ---------------------------------------------------------------
  // Per-Speaker Hysteresis — Attendee's ParticipantSpeechStartStopManager
  //
  // 1 sample to start speaking, 2 samples (~600ms) to stop.
  // Prevents flickering on short pauses mid-sentence.
  // ---------------------------------------------------------------
  var HYSTERESIS_START = 1;   // samples needed to consider "speaking"
  var HYSTERESIS_STOP  = 2;   // samples needed to consider "stopped" (~600ms at 300ms polling)
  var SPEAKER_TIMEOUT  = 10000; // 10s — clear stale entries (WebRTC spec: CSRC valid for 10s)

  function updateSpeakerHysteresis(contributingSources) {
    var now = Date.now();
    var activeSources = new Set();

    for (var i = 0; i < contributingSources.length; i++) {
      var src = contributingSources[i];
      var sid = src.source.toString();
      activeSources.add(sid);

      var entry = state.speakerState.get(sid);
      var isSpeaking = typeof src.audioLevel === 'number' && src.audioLevel >= 0.01;

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
          // Per-participant audio: feed attribution buffer on speaking START (Faz 9)
          if (window.__miaAttributionBuffer) {
            var user = getUserByStreamId(sid);
            if (user) {
              window.__miaAttributionBuffer.updateSpeaker(user.deviceId || sid, user.displayName, true, now);
            }
          }
        }
      } else {
        entry.stopCount++;
        entry.startCount = 0;
        if (entry.speaking && entry.stopCount >= HYSTERESIS_STOP) {
          entry.speaking = false;
          // Per-participant audio: feed attribution buffer on speaking STOP (Faz 9)
          if (window.__miaAttributionBuffer) {
            var user = getUserByStreamId(sid);
            if (user) {
              window.__miaAttributionBuffer.updateSpeaker(user.deviceId || sid, user.displayName, false, now);
            }
          }
        }
      }
    }

    // Prune stale entries (not seen in 10s)
    state.speakerState.forEach(function(v, k) {
      if (now - v.lastSeen > SPEAKER_TIMEOUT) {
        state.speakerState.delete(k);
      }
    });
  }

  // ---------------------------------------------------------------
  // RTCPeerConnection Patch — Reflect.construct (Attendee pattern)
  //
  // Attendee uses Reflect.construct to preserve the correct prototype
  // chain and new.target behavior.
  // ---------------------------------------------------------------
  var OrigPC = window.RTCPeerConnection;

  window.RTCPeerConnection = function() {
    var pc = Reflect.construct(OrigPC, Array.prototype.slice.call(arguments));
    state.peerConnections.push(pc);
    addLog('RTCPeerConnection created (total: ' + state.peerConnections.length + ')');

    // Intercept incoming audio tracks (Attendee pattern)
    // Register receivers immediately when tracks arrive — don't wait for polling
    pc.addEventListener('track', function(event) {
      try {
        if (event.receiver && event.track && event.track.kind === 'audio') {
          state.audioReceivers.add(event.receiver);
          addLog('Audio track registered via ontrack (total: ' + state.audioReceivers.size + ')');

          // Proactively call getContributingSources to warm the cache
          // (triggers our prototype intercept → caches CSRC immediately)
          var cs = event.receiver.getContributingSources();
          var ss = event.receiver.getSynchronizationSources();
          if (cs.length > 0 || ss.length > 0) {
            addLog('ontrack: immediate CSRC=' + cs.length + ' SSRC=' + ss.length);
          }

          // Per-participant audio: connect track to attribution buffer (Faz 9)
          // Buffer captures audio frames via MediaStreamTrackProcessor
          if (window.__miaAttributionBuffer) {
            window.__miaAttributionBuffer.connectAudioTrack(event.track);
            addLog('Audio track connected to attribution buffer');
          }
        }
      } catch(e) {}
    });

    // Intercept incoming remote data channels (collections is remote)
    pc.addEventListener('datachannel', function(event) {
      var channel = event.channel;
      channel.binaryType = 'arraybuffer';

      addLog('datachannel event: ' + channel.label);

      if (channel.label === 'collections') {
        state.collectionsChannelFound = true;
        addLog('collections data channel intercepted');
        channel.addEventListener('message', function(e) {
          handleCollectionEvent(e);
        });
      }
    });

    // Intercept createDataChannel (outgoing local channels)
    var origCreateDC = pc.createDataChannel.bind(pc);
    pc.createDataChannel = function(label, options) {
      var dc = origCreateDC(label, options);
      dc.binaryType = 'arraybuffer';

      addLog('createDataChannel: ' + label);

      if (dc.label === 'collections') {
        state.collectionsChannelFound = true;
        addLog('collections data channel created (local)');
        dc.addEventListener('message', function(e) {
          handleCollectionEvent(e);
        });
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
  // Collect all audio receivers — merge pc.getReceivers() + track-registered
  // ---------------------------------------------------------------
  function getAllAudioReceivers() {
    var receivers = new Set();

    // From track events (Attendee pattern — registered immediately)
    state.audioReceivers.forEach(function(r) {
      if (r.track && r.track.readyState !== 'ended') {
        receivers.add(r);
      }
    });

    // From pc.getReceivers() (fallback)
    for (var p = 0; p < state.peerConnections.length; p++) {
      var pc = state.peerConnections[p];
      if (pc.connectionState === 'closed') continue;
      try {
        var pcReceivers = pc.getReceivers();
        for (var r = 0; r < pcReceivers.length; r++) {
          var receiver = pcReceivers[r];
          if (receiver.track && receiver.track.kind === 'audio' && receiver.track.readyState !== 'ended') {
            receivers.add(receiver);
          }
        }
      } catch(e) {}
    }

    return receivers;
  }

  // ---------------------------------------------------------------
  // Check CSRC sources for a single receiver (direct + cached)
  // Returns array of { streamId, audioLevel, user }
  // ---------------------------------------------------------------
  function getSourcesForReceiver(receiver) {
    var sources = [];

    // 1. Direct call (triggers prototype intercept → cache update)
    try {
      var contribs = receiver.getContributingSources();
      for (var c = 0; c < contribs.length; c++) {
        var src = contribs[c];
        sources.push({ streamId: src.source.toString(), audioLevel: src.audioLevel || 0, ts: src.timestamp });
      }
    } catch(e) {}

    // 2. Cached CSRC from Google Meet's own internal calls
    var cached = state.receiverMap.get(receiver);
    if (cached) {
      var seenIds = new Set(sources.map(function(s) { return s.streamId; }));
      for (var c = 0; c < cached.length; c++) {
        var src = cached[c];
        var sid = src.source.toString();
        if (!seenIds.has(sid)) {
          sources.push({ streamId: sid, audioLevel: src.audioLevel || 0, ts: src.timestamp });
        }
      }
    }

    return sources;
  }

  // ---------------------------------------------------------------
  // Speaker Detection — synchronous, called by polling loop (300ms)
  //
  // Attendee chain: getContributingSources() → source.source.toString()
  //   → getUserByStreamId() → deviceOutputMap → allUsersMap → displayName
  //
  // Enhanced with:
  //   - track-registered receivers (don't miss newly added tracks)
  //   - per-speaker hysteresis (1 start / 4 stop — Attendee pattern)
  //   - getSynchronizationSources SSRC fallback
  //   - stale cache cleanup
  // ---------------------------------------------------------------
  window.__miaWebrtcDetectSpeaker = function() {
    // Prune closed peer connections
    state.peerConnections = state.peerConnections.filter(function(pc) {
      return pc.connectionState !== 'closed';
    });

    // Prune ended receivers from track-registered set
    state.audioReceivers.forEach(function(r) {
      if (!r.track || r.track.readyState === 'ended') {
        state.audioReceivers.delete(r);
      }
    });

    var bestSpeaker = null;
    var bestLevel = 0;
    var receivers = getAllAudioReceivers();

    // --- PRIMARY: CSRC-based detection with hysteresis ---
    receivers.forEach(function(receiver) {
      var sources = getSourcesForReceiver(receiver);

      for (var i = 0; i < sources.length; i++) {
        var src = sources[i];
        // Use hysteresis state if available (more stable than raw audioLevel)
        var hState = state.speakerState.get(src.streamId);

        if (hState && hState.speaking && src.audioLevel > bestLevel) {
          var user = getUserByStreamId(src.streamId);
          if (user) {
            bestSpeaker = user.displayName;
            bestLevel = src.audioLevel;
          }
        } else if (!hState && src.audioLevel >= 0.01 && src.audioLevel > bestLevel) {
          // No hysteresis entry yet — use raw threshold (first detection)
          var user = getUserByStreamId(src.streamId);
          if (user) {
            bestSpeaker = user.displayName;
            bestLevel = src.audioLevel;
          }
        }
      }
    });

    if (bestSpeaker) {
      return { name: bestSpeaker, source: 'webrtc' };
    }

    // --- FALLBACK: SSRC audioLevel (when CSRC is empty) ---
    // Google Meet sends 3 SSRCs for loudest speakers.
    // Even without CSRC, SSRC audioLevel tells us which stream is active.
    // Combined with deviceOutputMap we might still identify the speaker.
    receivers.forEach(function(receiver) {
      if (bestSpeaker) return; // already found via CSRC

      // Call getSynchronizationSources (triggers prototype intercept)
      try {
        var syncSources = receiver.getSynchronizationSources();
        if (!syncSources || syncSources.length === 0) {
          syncSources = state.syncSourceMap.get(receiver) || [];
        }

        for (var s = 0; s < syncSources.length; s++) {
          var ssrc = syncSources[s];
          if (typeof ssrc.audioLevel === 'number' && ssrc.audioLevel >= 0.01 && ssrc.audioLevel > bestLevel) {
            // Try to map SSRC source to user via deviceOutputMap
            var user = getUserByStreamId(ssrc.source.toString());
            if (user) {
              bestSpeaker = user.displayName;
              bestLevel = ssrc.audioLevel;
            }
          }
        }
      } catch(e) {}
    });

    if (bestSpeaker) {
      return { name: bestSpeaker, source: 'webrtc-ssrc' };
    }

    return null;
  };

  // ---------------------------------------------------------------
  // Diagnostic — expose state for debugging
  // ---------------------------------------------------------------
  window.__miaWebrtcDiag = function() {
    var userEntries = [];
    state.allUsersMap.forEach(function(v, k) {
      userEntries.push(k.substring(k.lastIndexOf('/') + 1) + ' -> ' + v.displayName);
    });

    var streamEntries = [];
    state.deviceOutputMap.forEach(function(v) {
      streamEntries.push('stream:' + v.streamId + ' -> device:' + v.deviceId.substring(v.deviceId.lastIndexOf('/') + 1) + ' type:' + v.outputType);
    });

    var totalAudioReceivers = 0;
    var receiversWithCsrc = 0;
    var receiversWithSsrc = 0;
    var trackRegisteredCount = state.audioReceivers.size;

    state.peerConnections.forEach(function(pc) {
      if (pc.connectionState === 'closed') return;
      try {
        var receivers = pc.getReceivers();
        for (var r = 0; r < receivers.length; r++) {
          if (receivers[r].track && receivers[r].track.kind === 'audio') {
            totalAudioReceivers++;
            if (state.receiverMap.has(receivers[r])) receiversWithCsrc++;
            if (state.syncSourceMap.has(receivers[r])) receiversWithSsrc++;
          }
        }
      } catch(e) {}
    });

    // Speaker hysteresis state
    var speakerStates = [];
    state.speakerState.forEach(function(v, k) {
      var user = getUserByStreamId(k);
      speakerStates.push({
        streamId: k,
        user: user ? user.displayName : '(unknown)',
        speaking: v.speaking,
        audioLevel: v.audioLevel,
        startCount: v.startCount,
        stopCount: v.stopCount,
        ageSec: ((Date.now() - v.lastSeen) / 1000).toFixed(1),
      });
    });

    // Live SSRC snapshot
    var ssrcSnapshot = [];
    state.syncSourceMap.forEach(function(entries, receiver) {
      for (var i = 0; i < entries.length; i++) {
        var e = entries[i];
        ssrcSnapshot.push({
          ssrc: e.source,
          audioLevel: e.audioLevel,
          user: getUserByStreamId(e.source.toString()) ? getUserByStreamId(e.source.toString()).displayName : null,
        });
      }
    });

    return {
      peerConnections: state.peerConnections.length,
      collectionsChannelFound: state.collectionsChannelFound,
      userMapSize: state.allUsersMap.size,
      userMapSample: userEntries.slice(0, 10),
      deviceOutputMapSize: state.deviceOutputMap.size,
      deviceOutputMapSample: streamEntries.slice(0, 10),
      totalAudioReceivers: totalAudioReceivers,
      trackRegisteredReceivers: trackRegisteredCount,
      receiversWithCsrc: receiversWithCsrc,
      receiversWithSsrc: receiversWithSsrc,
      speakerHysteresis: speakerStates,
      ssrcSnapshot: ssrcSnapshot,
      recentLog: state.log.slice(-15),
    };
  };

  // ---------------------------------------------------------------
  // Deep Debug — getStats() based RTP analysis
  //
  // getStats() returns raw RTP stats that Chrome collects regardless
  // of whether getContributingSources() works. This tells us:
  //   1. Are CSRC values present in RTP packets at all?
  //   2. How many audio streams/SSRCs are active?
  //   3. Is audio actually flowing (packetsReceived increasing)?
  //   4. What codecs are negotiated?
  //   5. SDP offer/answer contents (audio transceiver config)
  //
  // Call: await __miaWebrtcDeepDebug() (async!)
  // ---------------------------------------------------------------
  window.__miaWebrtcDeepDebug = async function() {
    var report = {
      timestamp: new Date().toISOString(),
      peerConnections: [],
    };

    for (var p = 0; p < state.peerConnections.length; p++) {
      var pc = state.peerConnections[p];
      if (pc.connectionState === 'closed') continue;

      var pcReport = {
        index: p,
        connectionState: pc.connectionState,
        iceConnectionState: pc.iceConnectionState,
        signalingState: pc.signalingState,
        audioInboundRtp: [],
        audioTransceivers: [],
        sdpOffer: null,
        sdpAnswer: null,
        csrcLiveCheck: [],
        ssrcLiveCheck: [],
      };

      // --- 1. SDP Analysis (how audio was negotiated) ---
      try {
        if (pc.localDescription && pc.localDescription.sdp) {
          // Extract just audio m-lines
          var sdp = pc.localDescription.sdp;
          var audioLines = [];
          var inAudio = false;
          sdp.split('\\n').forEach(function(line) {
            if (line.startsWith('m=audio')) { inAudio = true; audioLines.push('--- AUDIO SECTION ---'); }
            else if (line.startsWith('m=')) { inAudio = false; }
            if (inAudio) audioLines.push(line.trim());
          });
          pcReport.sdpOffer = audioLines.slice(0, 30);
        }
        if (pc.remoteDescription && pc.remoteDescription.sdp) {
          var sdp = pc.remoteDescription.sdp;
          var audioLines = [];
          var inAudio = false;
          sdp.split('\\n').forEach(function(line) {
            if (line.startsWith('m=audio')) { inAudio = true; audioLines.push('--- AUDIO SECTION ---'); }
            else if (line.startsWith('m=')) { inAudio = false; }
            if (inAudio) audioLines.push(line.trim());
          });
          pcReport.sdpAnswer = audioLines.slice(0, 30);
        }
      } catch(e) {}

      // --- 2. getStats() — inbound-rtp for audio ---
      try {
        var stats = await pc.getStats();
        stats.forEach(function(stat) {
          if (stat.type === 'inbound-rtp' && stat.kind === 'audio') {
            pcReport.audioInboundRtp.push({
              ssrc: stat.ssrc,
              packetsReceived: stat.packetsReceived,
              bytesReceived: stat.bytesReceived,
              packetsLost: stat.packetsLost,
              jitter: stat.jitter,
              // contributingSources — Chrome exposes this in stats too
              contributingSourcesCount: stat.contributingSources ? stat.contributingSources.length : 'N/A',
              contributingSources: stat.contributingSources || 'N/A',
              codecId: stat.codecId,
              trackIdentifier: stat.trackIdentifier,
              // Check if audio is flowing
              lastPacketReceivedTimestamp: stat.lastPacketReceivedTimestamp,
              headerBytesReceived: stat.headerBytesReceived,
              // Audio level from stats
              audioLevel: stat.audioLevel,
              totalAudioEnergy: stat.totalAudioEnergy,
              totalSamplesDuration: stat.totalSamplesDuration,
            });
          }
          // Codec info
          if (stat.type === 'codec' && stat.mimeType && stat.mimeType.startsWith('audio/')) {
            pcReport.audioInboundRtp.push({
              _type: 'codec',
              codecId: stat.id,
              mimeType: stat.mimeType,
              clockRate: stat.clockRate,
              channels: stat.channels,
              sdpFmtpLine: stat.sdpFmtpLine,
            });
          }
        });
      } catch(e) {
        pcReport.statsError = e.message || String(e);
      }

      // --- 3. Live CSRC/SSRC check on every audio receiver ---
      try {
        var receivers = pc.getReceivers();
        for (var r = 0; r < receivers.length; r++) {
          var recv = receivers[r];
          if (!recv.track || recv.track.kind !== 'audio') continue;

          var csrc = recv.getContributingSources();
          var ssrc = recv.getSynchronizationSources();

          pcReport.csrcLiveCheck.push({
            receiverIndex: r,
            trackId: recv.track.id,
            trackState: recv.track.readyState,
            trackMuted: recv.track.muted,
            csrcCount: csrc.length,
            csrcData: csrc.map(function(c) {
              return { source: c.source, audioLevel: c.audioLevel, timestamp: c.timestamp };
            }),
            ssrcCount: ssrc.length,
            ssrcData: ssrc.map(function(s) {
              return { source: s.source, audioLevel: s.audioLevel, timestamp: s.timestamp };
            }),
          });
        }
      } catch(e) {}

      // --- 4. Transceiver analysis ---
      try {
        var transceivers = pc.getTransceivers();
        for (var t = 0; t < transceivers.length; t++) {
          var tr = transceivers[t];
          if (tr.receiver && tr.receiver.track && tr.receiver.track.kind === 'audio') {
            pcReport.audioTransceivers.push({
              mid: tr.mid,
              direction: tr.direction,
              currentDirection: tr.currentDirection,
              stopped: tr.stopped,
            });
          }
        }
      } catch(e) {}

      report.peerConnections.push(pcReport);
    }

    // --- 5. Google Meet internal call tracking ---
    // Check if Meet's own code called getContributingSources since last check
    report.prototypeInterceptStats = {
      receiverMapSize: state.receiverMap.size,
      syncSourceMapSize: state.syncSourceMap.size,
      speakerStateSize: state.speakerState.size,
      audioReceiversRegistered: state.audioReceivers.size,
    };

    return report;
  };

  console.log('[MiaWebRTC] Interceptor installed (Attendee-aligned v3: track events + SSRC fallback + hysteresis)');
})();
`;
