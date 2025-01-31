package setting

import "strings"

var CheckSensitiveEnabled = false
var CheckSensitiveOnPromptEnabled = false

//var CheckSensitiveOnCompletionEnabled = true

// StopOnSensitiveEnabled 如果检测到敏感词，是否立刻停止生成，否则替换敏感词
var StopOnSensitiveEnabled = false

// StreamCacheQueueLength 流模式缓存队列长度，0表示无缓存
var StreamCacheQueueLength = 0

// SensitiveWords 敏感词
// var SensitiveWords []string
var SensitiveWords = []string{
	"test_sensitive",
}

var ImageBase64Enabled = true
var ImageDomainWhitelist = []string{
	"aliyuncs.com",
}

func SensitiveWordsToString() string {
	return strings.Join(SensitiveWords, "\n")
}

func SensitiveWordsFromString(s string) {
	SensitiveWords = []string{}
	sw := strings.Split(s, "\n")
	for _, w := range sw {
		w = strings.TrimSpace(w)
		if w != "" {
			SensitiveWords = append(SensitiveWords, w)
		}
	}
}

func ImageDomainWhitelistToString() string {
	return strings.Join(ImageDomainWhitelist, "\n")
}

func ImageDomainWhitelistFromString(s string) {
	ImageDomainWhitelist = []string{}
	domains := strings.Split(s, "\n")
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain != "" {
			ImageDomainWhitelist = append(ImageDomainWhitelist, domain)
		}
	}
}

func ShouldCheckPromptSensitive() bool {
	return CheckSensitiveEnabled && CheckSensitiveOnPromptEnabled
}

//func ShouldCheckCompletionSensitive() bool {
//	return CheckSensitiveEnabled && CheckSensitiveOnCompletionEnabled
//}
