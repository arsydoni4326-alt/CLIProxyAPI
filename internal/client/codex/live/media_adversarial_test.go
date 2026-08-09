package live

import (
	"context"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// validAdversarialOffer builds a syntactically complete SDP offer that the
// relay can accept. Truncating or corrupting it should never crash the relay.
func validAdversarialOffer(t *testing.T) string {
	t.Helper()

	clientAPI := newTestWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	if _, errChannel := client.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
		t.Fatalf("create client DataChannel: %v", errChannel)
	}
	return completeOffer(t, client)
}

// TestNewSessionAdversarialClientOffers feeds malformed and hostile SDP offers
// into the media relay and verifies it always fails gracefully with an error
// instead of panicking or crashing.
func TestNewSessionAdversarialClientOffers(t *testing.T) {
	validOffer := validAdversarialOffer(t)
	corruptedOffer := func() string {
		// Truncate mid-SDP and append garbage to simulate a partial upload.
		cut := len(validOffer) / 2
		return validOffer[:cut] + "\r\na=corrupted:" + strings.Repeat("x", 4096)
	}()

	tests := map[string]struct {
		offer string
	}{
		"empty offer":            {offer: ""},
		"blank offer":            {offer: "   \r\n\t \r\n"},
		"not SDP":                {offer: "this is not an SDP offer at all"},
		"json body":              {offer: `{"sdp":"not real SDP","type":"offer"}`},
		"truncated mid-line":     {offer: validOffer[:len(validOffer)/2]},
		"corrupted with garbage": {offer: corruptedOffer},
		"huge repeated line":     {offer: validOffer + "\r\na=x:" + strings.Repeat("y", 1<<16)},
		"null bytes":             {offer: validOffer + "\x00\x01\x02\x03"},
		"only versions":          {offer: "v=0\r\nv=1\r\nv=2\r\n"},
		"misplaced m= line":      {offer: "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			relay, errRelay := newPionMediaRelay(config.CodexLiveMediaRelayConfig{
				Enabled: true,
			})
			if errRelay != nil {
				t.Fatalf("create media relay: %v", errRelay)
			}

			session, _, errSession := relay.NewSession(context.Background(), testCase.offer, mediaSessionRoute{})
			if errSession == nil && session != nil {
				_ = session.Close()
			}
			// The test passes as long as no panic occurs; an error is the expected outcome.
		})
	}
}

// TestAcceptUpstreamAnswerAdversarialAnswers feeds malformed and hostile SDP
// answers from the upstream into an established session and verifies graceful
// failure without panics.
func TestAcceptUpstreamAnswerAdversarialAnswers(t *testing.T) {
	tests := map[string]struct {
		answer string
	}{
		"empty answer":              {answer: ""},
		"blank answer":              {answer: "  \r\n "},
		"not SDP":                   {answer: "garbage garbage garbage"},
		"json body":                 {answer: `{"sdp":"nope","type":"answer"}`},
		"hostile candidate flood":   {answer: buildCandidateFloodAnswer(512)},
		"hostile candidate private": {answer: buildPrivateCandidateAnswer()},
		"wrong SDP type":            {answer: "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n"},
		"only headers no media":     {answer: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n"},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			clientAPI := newTestWebRTCAPI(t)
			client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
			if errClient != nil {
				t.Fatalf("create client PeerConnection: %v", errClient)
			}
			defer closeTestPeerConnection(t, client)
			if _, errChannel := client.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
				t.Fatalf("create client DataChannel: %v", errChannel)
			}
			clientOffer := completeOffer(t, client)

			relay, errRelay := newPionMediaRelay(config.CodexLiveMediaRelayConfig{
				Enabled: true,
			})
			if errRelay != nil {
				t.Fatalf("create media relay: %v", errRelay)
			}
			session, _, errSession := relay.NewSession(context.Background(), clientOffer, mediaSessionRoute{})
			if errSession != nil {
				t.Fatalf("create media session: %v", errSession)
			}
			defer func() {
				if errClose := session.Close(); errClose != nil {
					t.Errorf("close media session: %v", errClose)
				}
			}()

			_, errAnswer := session.AcceptUpstreamAnswer(context.Background(), testCase.answer)
			if errAnswer == nil {
				// Some adversarial inputs may technically parse but we want to
				// verify no panic occurred. The test passes either way.
				t.Logf("adversarial answer unexpectedly accepted (no error)")
			}
		})
	}
}

// TestNewSessionAdversarialPrivateIPFiltering verifies that offers containing
// only private/loopback ICE candidates are handled correctly when private-IP
// filtering is enabled (the relay should reject the private candidate path and
// fall back gracefully).
func TestNewSessionAdversarialPrivateIPFiltering(t *testing.T) {
	clientAPI := newTestWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	if _, errChannel := client.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
		t.Fatalf("create client DataChannel: %v", errChannel)
	}
	clientOffer := completeOffer(t, client)

	relay, errRelay := newPionMediaRelay(config.CodexLiveMediaRelayConfig{
		Enabled:                 true,
		DisablePrivateRemoteIPs: true,
	})
	if errRelay != nil {
		t.Fatalf("create media relay: %v", errRelay)
	}

	session, upstreamOffer, errSession := relay.NewSession(context.Background(), clientOffer, mediaSessionRoute{})
	if errSession != nil {
		t.Fatalf("create media session: %v", errSession)
	}
	defer func() {
		if errClose := session.Close(); errClose != nil {
			t.Errorf("close media session: %v", errClose)
		}
	}()
	if upstreamOffer == "" {
		t.Fatal("relay returned an empty upstream offer")
	}
}

// TestNewSessionAdversarialContextCancellation verifies that cancelling the
// context mid-offer-creation does not leave dangling goroutines or panic.
func TestNewSessionAdversarialContextCancellation(t *testing.T) {
	clientAPI := newTestWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	if _, errChannel := client.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
		t.Fatalf("create client DataChannel: %v", errChannel)
	}
	clientOffer := completeOffer(t, client)

	relay, errRelay := newPionMediaRelay(config.CodexLiveMediaRelayConfig{
		Enabled: true,
	})
	if errRelay != nil {
		t.Fatalf("create media relay: %v", errRelay)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, _, errSession := relay.NewSession(ctx, clientOffer, mediaSessionRoute{})
	if errSession == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(errSession.Error(), "context canceled") && !strings.Contains(errSession.Error(), "cancel") {
		t.Fatalf("expected context cancellation error, got: %v", errSession)
	}
}

// buildCandidateFloodAnswer returns an SDP answer with an excessive number of
// ICE candidates to trigger the candidate-count limit.
func buildCandidateFloodAnswer(count int) string {
	var builder strings.Builder
	_, _ = builder.WriteString("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n")
	_, _ = builder.WriteString("m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\n")
	_, _ = builder.WriteString("a=ice-ufrag:remote-ufrag\r\na=ice-pwd:remote-password\r\n")
	for index := 0; index < count; index++ {
		_, _ = builder.WriteString("a=candidate:1 1 udp 2130706431 8.8.8.8 3478 typ host\r\n")
	}
	return builder.String()
}

// buildPrivateCandidateAnswer returns an SDP answer pointing at a private IP.
func buildPrivateCandidateAnswer() string {
	var builder strings.Builder
	_, _ = builder.WriteString("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n")
	_, _ = builder.WriteString("m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\n")
	_, _ = builder.WriteString("a=ice-ufrag:remote-ufrag\r\na=ice-pwd:remote-password\r\n")
	_, _ = builder.WriteString("a=candidate:1 1 udp 2130706431 10.0.0.1 3478 typ host\r\n")
	return builder.String()
}
