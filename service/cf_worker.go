package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"one-api/common"
	"one-api/setting"
	"strings"
	"time"
)

func DoDownloadRequest(originUrl string) (resp *http.Response, err error) {
	maxImageSize := int64(common.MaxImageSize * 1024 * 1024)
	requestTimeout := common.RequestTimeout

	client := &http.Client{}
	if requestTimeout > 0 {
		client.Timeout = time.Duration(requestTimeout) * time.Second
	}

	// 定义通用headers
	headers := map[string]string{
		"User-Agent":                  "OpenAI Image Downloader",
		"Accept":                      "*/*",
		"Traceparent":                 "00-676cffe5000000002d5afb66c8b27710-33d22fd10b336f72-00",
		"Tracestate":                  "dd=p:33d22fd10b336f72;s:0;t.dm:-3;t.tid:676cffe500000000",
		"X-Datadog-Parent-Id":         "3734099615926153074",
		"X-Datadog-Sampling-Priority": "0",
		"X-Datadog-Tags":              "_dd.p.tid=676cffe500000000,_dd.p.dm=-3",
		"X-Datadog-Trace-Id":          "3268200898483091216",
		"X-Openai-Internal-Caller":    "mmapi",
		"X-Openai-Originator":         "",
		"X-Openai-Originator-Env":     "",
		"X-Openai-Traffic-Source":     "",
		"Accept-Encoding":             "gzip",
	}

	if setting.EnableWorker() {
		common.SysLog(fmt.Sprintf("downloading file from worker: %s", originUrl))
		if !strings.HasPrefix(originUrl, "https") {
			return nil, fmt.Errorf("only support https url")
		}
		workerUrl := setting.WorkerUrl
		if !strings.HasSuffix(workerUrl, "/") {
			workerUrl += "/"
		}
		data := []byte(`{"url":"` + originUrl + `","key":"` + setting.WorkerValidKey + `"}`)
		resp, err = client.Post(workerUrl, "application/json", bytes.NewBuffer(data))
		if err != nil {
			return nil, err
		}
	} else {
		common.SysLog(fmt.Sprintf("downloading from origin: %s", originUrl))
		req, err := http.NewRequest(http.MethodGet, originUrl, nil)
		if err != nil {
			return nil, err
		}
		// 设置headers
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}
	}

	// 如果未设置最大图片大小，则直接返回响应
	if maxImageSize <= 0 {
		return resp, nil
	}

	// 使用io.LimitReader限制读取的字节数
	limitedReader := io.LimitReader(resp.Body, maxImageSize+1) // 读多一个字节以检测是否超出限制

	// 读取数据到缓冲区
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("error reading response body: %v", err)
	}

	// 检查是否超过最大图片大小
	if int64(len(data)) > maxImageSize {
		resp.Body.Close()
		return nil, fmt.Errorf("file size exceeds maximum allowed size of %d bytes", maxImageSize)
	}

	// 将读取的数据重新封装到新的ReadCloser中
	resp.Body = io.NopCloser(bytes.NewBuffer(data))

	return resp, nil
}
