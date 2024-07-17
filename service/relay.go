package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"one-api/common"
)

func SetEventStreamHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

func StringData(c *gin.Context, str string) error {
	//str = strings.TrimPrefix(str, "data: ")
	//str = strings.TrimSuffix(str, "\r")
	c.Render(-1, common.CustomEvent{Data: "data: " + str})
	if c.Writer != nil {
		c.Writer.Flush()
	} else {
		return errors.New("writer is nil")
	}
	return nil
}

func ObjectData(c *gin.Context, object interface{}) error {
	jsonData, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	return StringData(c, string(jsonData))
}

func Done(c *gin.Context) {
	_ = StringData(c, "[DONE]")
}

func GetResponseID(c *gin.Context) string {
	logID := c.GetString("X-Oneapi-Request-Id")
	return fmt.Sprintf("chatcmpl-%s", logID)
}