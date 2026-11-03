package plugins_test

import (
	"testing"
)

func TestOpenAICompatibleVideoResponses(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey:  "oai-video",
		model:      "video-1",
		wantAction: "text_to_video",
		requestBody: map[string]any{
			"model":   "video-1",
			"input":   "a cat walking on the beach",
			"seconds": float64(8),
			"size":    "1280x720",
		},
		wantRequest: map[string]any{
			"prompt":  "a cat walking on the beach",
			"seconds": float64(8),
			"size":    "1280x720",
		},
		wantUsageKeys:  []string{"seconds", "size"},
		wantVendorName: "oai-video",
	})
}
