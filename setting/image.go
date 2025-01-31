package setting

import "strings"

var ImageBase64Enabled = true
var ImageDomainWhitelist = []string{
	"aliyuncs.com",
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
