package register

import "gpt-image-web/internal/upstream/chatgpt"

func chatgptBuildProofTokenInternal(seed string, difficulty string, userAgent string) (string, error) {
	return chatgpt.BuildProofToken(seed, difficulty, userAgent)
}
