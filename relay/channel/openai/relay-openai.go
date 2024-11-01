package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"one-api/common"
	"one-api/constant"
	"one-api/dto"
	relaycommon "one-api/relay/common"
	relayconstant "one-api/relay/constant"
	"one-api/service"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

func OaiStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	containStreamUsage := false
	var responseId string
	var createAt int64 = 0
	var systemFingerprint string
	model := info.UpstreamModelName

	var responseTextBuilder strings.Builder
	var usage = &dto.Usage{}
	var streamItems []string // store stream items

	toolCount := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(bufio.ScanLines)

	service.SetEventStreamHeaders(c)

	ticker := time.NewTicker(time.Duration(constant.StreamingTimeout) * time.Second)
	defer ticker.Stop()

	stopChan := make(chan bool)
	defer close(stopChan)
	var (
		lastStreamData string
		mu             sync.Mutex
	)
	gopool.Go(func() {
		for scanner.Scan() {
			info.SetFirstResponseTime()
			ticker.Reset(time.Duration(constant.StreamingTimeout) * time.Second)
			data := scanner.Text()
			if len(data) < 6 { // ignore blank line or wrong format
				continue
			}
			if data[:6] != "data: " && data[:6] != "[DONE]" {
				continue
			}
			mu.Lock()
			data = data[6:]
			if !strings.HasPrefix(data, "[DONE]") {
				if lastStreamData != "" {
					err := service.StringData(c, lastStreamData)
					if err != nil {
						common.LogError(c, "streaming error: "+err.Error())
					}
				}
				lastStreamData = data
				streamItems = append(streamItems, data)
			}
			mu.Unlock()
		}
		common.SafeSendBool(stopChan, true)
	})

	select {
	case <-ticker.C:
		// 超时处理逻辑
		common.LogError(c, "streaming timeout")
	case <-stopChan:
		// 正常结束
	}

	shouldSendLastResp := true
	var lastStreamResponse dto.ChatCompletionsStreamResponse
	err := json.Unmarshal(common.StringToByteSlice(lastStreamData), &lastStreamResponse)
	if err == nil {
		responseId = lastStreamResponse.Id
		createAt = lastStreamResponse.Created
		systemFingerprint = lastStreamResponse.GetSystemFingerprint()
		model = lastStreamResponse.Model
		if service.ValidUsage(lastStreamResponse.Usage) {
			containStreamUsage = true
			usage = lastStreamResponse.Usage
			if !info.ShouldIncludeUsage {
				shouldSendLastResp = false
			}
		}
	}
	if shouldSendLastResp {
		service.StringData(c, lastStreamData)
	}

	// 计算token
	streamResp := "[" + strings.Join(streamItems, ",") + "]"
	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions:
		var streamResponses []dto.ChatCompletionsStreamResponse
		err := json.Unmarshal(common.StringToByteSlice(streamResp), &streamResponses)
		if err != nil {
			// 一次性解析失败，逐个解析
			common.SysError("error unmarshalling stream response: " + err.Error())
			for _, item := range streamItems {
				var streamResponse dto.ChatCompletionsStreamResponse
				err := json.Unmarshal(common.StringToByteSlice(item), &streamResponse)
				if err == nil {
					//if service.ValidUsage(streamResponse.Usage) {
					//	usage = streamResponse.Usage
					//}
					for _, choice := range streamResponse.Choices {
						responseTextBuilder.WriteString(choice.Delta.GetContentString())
						if choice.Delta.ToolCalls != nil {
							if len(choice.Delta.ToolCalls) > toolCount {
								toolCount = len(choice.Delta.ToolCalls)
							}
							for _, tool := range choice.Delta.ToolCalls {
								responseTextBuilder.WriteString(tool.Function.Name)
								responseTextBuilder.WriteString(tool.Function.Arguments)
							}
						}
					}
				}
			}
		} else {
			for _, streamResponse := range streamResponses {
				//if service.ValidUsage(streamResponse.Usage) {
				//	usage = streamResponse.Usage
				//	containStreamUsage = true
				//}
				for _, choice := range streamResponse.Choices {
					responseTextBuilder.WriteString(choice.Delta.GetContentString())
					if choice.Delta.ToolCalls != nil {
						if len(choice.Delta.ToolCalls) > toolCount {
							toolCount = len(choice.Delta.ToolCalls)
						}
						for _, tool := range choice.Delta.ToolCalls {
							responseTextBuilder.WriteString(tool.Function.Name)
							responseTextBuilder.WriteString(tool.Function.Arguments)
						}
					}
				}
			}
		}
	case relayconstant.RelayModeCompletions:
		var streamResponses []dto.CompletionsStreamResponse
		err := json.Unmarshal(common.StringToByteSlice(streamResp), &streamResponses)
		if err != nil {
			// 一次性解析失败，逐个解析
			common.SysError("error unmarshalling stream response: " + err.Error())
			for _, item := range streamItems {
				var streamResponse dto.CompletionsStreamResponse
				err := json.Unmarshal(common.StringToByteSlice(item), &streamResponse)
				if err == nil {
					for _, choice := range streamResponse.Choices {
						responseTextBuilder.WriteString(choice.Text)
					}
				}
			}
		} else {
			for _, streamResponse := range streamResponses {
				for _, choice := range streamResponse.Choices {
					responseTextBuilder.WriteString(choice.Text)
				}
			}
		}
	}

	if !containStreamUsage {
		usage, _ = service.ResponseText2Usage(responseTextBuilder.String(), info.UpstreamModelName, info.PromptTokens)
		usage.CompletionTokens += toolCount * 7
	}

	if info.ShouldIncludeUsage && !containStreamUsage {
		response := service.GenerateFinalUsageResponse(responseId, createAt, model, *usage)
		response.SetSystemFingerprint(systemFingerprint)
		service.ObjectData(c, response)
	}

	service.Done(c)

	resp.Body.Close()
	return nil, usage
}

func OpenaiHandler(c *gin.Context, resp *http.Response, promptTokens int, model string) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	defer resp.Body.Close()

	// 使用 json.RawMessage 保留原始 JSON
	var rawResponse map[string]json.RawMessage
	err = json.Unmarshal(responseBody, &rawResponse)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}

	// 解析错误和使用情况
	var simpleResponse dto.SimpleResponse
	err = json.Unmarshal(responseBody, &simpleResponse)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}

	// 处理响应体
	modifiedResponseBody, err := processResponseBody(responseBody, model, promptTokens, &simpleResponse)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "process_response_body_failed", http.StatusInternalServerError), nil
	}

	if simpleResponse.Error.Type != "" {
		return &dto.OpenAIErrorWithStatusCode{
			Error:      simpleResponse.Error,
			StatusCode: resp.StatusCode,
		}, nil
	}

	// 保持原始字段顺序，直接写入修改后的响应体
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = c.Writer.Write(modifiedResponseBody)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "write_response_body_failed", http.StatusInternalServerError), nil
	}

	// 处理 Usage 信息
	if simpleResponse.Usage.TotalTokens == 0 || (simpleResponse.Usage.PromptTokens == 0 && simpleResponse.Usage.CompletionTokens == 0) {
		completionTokens := 0
		for _, choice := range simpleResponse.Choices {
			ctkm, _ := service.CountTokenText(string(choice.Message.Content), model)
			completionTokens += ctkm
		}
		simpleResponse.Usage = dto.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
		if completionTokens == 0 {
			errMsg := "CompletionTokens is still zero after calculation."
			return &dto.OpenAIErrorWithStatusCode{
				Error: dto.OpenAIError{
					Message: errMsg,
					Type:    "rate_limit_exceeded",
					Param:   "",
					Code:    "",
				},
				StatusCode: http.StatusTooManyRequests, // 429 状态码
			}, nil
		}
	}

	return nil, &simpleResponse.Usage
}

func processResponseBody(responseBody []byte, model string, promptTokens int, simpleResponse *dto.SimpleResponse) ([]byte, error) {
	if strings.Contains(model, "o1") {
		common.SysLog("model name contains 'o1', delete content_filter_results")

		// 使用 json.RawMessage 保留其他字段顺序
		var responseMap map[string]json.RawMessage
		err := json.Unmarshal(responseBody, &responseMap)
		if err != nil {
			return nil, fmt.Errorf("unmarshal_response_body_to_map_failed: %w", err)
		}

		// 删除 choices 中的 content_filter_results
		if choices, ok := responseMap["choices"]; ok {
			var choicesArray []map[string]interface{}
			err := json.Unmarshal(choices, &choicesArray)
			if err != nil {
				return nil, fmt.Errorf("unmarshal_choices_failed: %w", err)
			}
			for i := range choicesArray {
				delete(choicesArray[i], "content_filter_results")
			}
			updatedChoices, err := json.Marshal(choicesArray)
			if err != nil {
				return nil, fmt.Errorf("marshal_updated_choices_failed: %w", err)
			}
			responseMap["choices"] = updatedChoices
		}

		// 删除顶层的 prompt_filter_results
		delete(responseMap, "prompt_filter_results")

		// 重新组装 JSON，保持原有顺序
		var buffer bytes.Buffer
		buffer.WriteString("{")
		first := true
		for k, v := range responseMap {
			if !first {
				buffer.WriteString(",")
			}
			first = false
			keyBytes, _ := json.Marshal(k)
			buffer.Write(keyBytes)
			buffer.WriteString(":")
			buffer.Write(v)
		}
		buffer.WriteString("}")

		return buffer.Bytes(), nil
	}

	if model == "gpt-4o-2024-08-06" {
		common.SysLog(fmt.Sprintf("raw model gpt-4o-2024-08-06, set prompt_tokens: %d, real prompt_tokens: %d", promptTokens, simpleResponse.Usage.PromptTokens))

		var responseMap map[string]json.RawMessage
		err := json.Unmarshal(responseBody, &responseMap)
		if err != nil {
			return nil, fmt.Errorf("unmarshal_response_body_failed: %w", err)
		}

		// 修改 usage.prompt_tokens
		if usage, ok := responseMap["usage"]; ok {
			var usageMap map[string]interface{}
			err := json.Unmarshal(usage, &usageMap)
			if err != nil {
				return nil, fmt.Errorf("unmarshal_usage_failed: %w", err)
			}
			usageMap["prompt_tokens"] = promptTokens
			updatedUsage, err := json.Marshal(usageMap)
			if err != nil {
				return nil, fmt.Errorf("marshal_updated_usage_failed: %w", err)
			}
			responseMap["usage"] = updatedUsage
		}

		// 重新组装 JSON，保持原有顺序
		var buffer bytes.Buffer
		buffer.WriteString("{")
		first := true
		for k, v := range responseMap {
			if !first {
				buffer.WriteString(",")
			}
			first = false
			keyBytes, _ := json.Marshal(k)
			buffer.Write(keyBytes)
			buffer.WriteString(":")
			buffer.Write(v)
		}
		buffer.WriteString("}")

		common.SysLog(fmt.Sprintf("changed model gpt-4o-2024-08-06, prompt_tokens: %d", promptTokens))
		return buffer.Bytes(), nil
	}

	return responseBody, nil
}

func OpenaiTTSHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	// Reset response body
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}

	usage := &dto.Usage{}
	usage.PromptTokens = info.PromptTokens
	usage.TotalTokens = info.PromptTokens
	return nil, usage
}

func OpenaiSTTHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, responseFormat string) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	var audioResp dto.AudioResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	err = json.Unmarshal(responseBody, &audioResp)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}

	// Reset response body
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	resp.Body.Close()

	var text string
	switch responseFormat {
	case "json":
		text, err = getTextFromJSON(responseBody)
	case "text":
		text, err = getTextFromText(responseBody)
	case "srt":
		text, err = getTextFromSRT(responseBody)
	case "verbose_json":
		text, err = getTextFromVerboseJSON(responseBody)
	case "vtt":
		text, err = getTextFromVTT(responseBody)
	}

	usage := &dto.Usage{}
	usage.PromptTokens = info.PromptTokens
	usage.CompletionTokens, _ = service.CountTokenText(text, info.UpstreamModelName)
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return nil, usage
}

func getTextFromVTT(body []byte) (string, error) {
	return getTextFromSRT(body)
}

func getTextFromVerboseJSON(body []byte) (string, error) {
	var whisperResponse dto.WhisperVerboseJSONResponse
	if err := json.Unmarshal(body, &whisperResponse); err != nil {
		return "", fmt.Errorf("unmarshal_response_body_failed err :%w", err)
	}
	return whisperResponse.Text, nil
}

func getTextFromSRT(body []byte) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	var builder strings.Builder
	var textLine bool
	for scanner.Scan() {
		line := scanner.Text()
		if textLine {
			builder.WriteString(line)
			textLine = false
			continue
		} else if strings.Contains(line, "-->") {
			textLine = true
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func getTextFromText(body []byte) (string, error) {
	return strings.TrimSuffix(string(body), "\n"), nil
}

func getTextFromJSON(body []byte) (string, error) {
	var whisperResponse dto.AudioResponse
	if err := json.Unmarshal(body, &whisperResponse); err != nil {
		return "", fmt.Errorf("unmarshal_response_body_failed err :%w", err)
	}
	return whisperResponse.Text, nil
}
