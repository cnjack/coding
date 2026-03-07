package prompts

import (
	"bytes"
	_ "embed"
	"text/template"
	"time"
)

//go:embed system.md
var systemPrompt string

func GetSystemPrompt(platform, pwd string) string {
	t, err := template.New("template").Parse(systemPrompt)
	if err != nil {
		return ""
	}
	var stringBuffer = bytes.NewBuffer(nil)
	err = t.Execute(stringBuffer, struct {
		Platform string
		Pwd      string
		Date     string
	}{
		Platform: platform,
		Pwd:      pwd,
		Date:     time.Now().Format("2006-01-02"),
	})
	if err != nil {
		return ""
	}
	return stringBuffer.String()
}
