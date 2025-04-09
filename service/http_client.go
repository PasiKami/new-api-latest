package service

import (
	"net/http"
	"one-api/common"
	"time"
)

var httpClient *http.Client
var impatientHTTPClient *http.Client
var streamhttpClient *http.Client
var VhttpClient *http.Client

func init() {
	if common.RelayTimeout == 0 {
		httpClient = &http.Client{}
	} else {
		httpClient = &http.Client{
			Timeout: time.Duration(common.RelayTimeout) * time.Second,
		}
	}
	if common.StreamRelayTimeout == 0 {
		streamhttpClient = &http.Client{}
	} else {
		streamhttpClient = &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: time.Duration(common.StreamRelayTimeout) * time.Second, // 设置首字响应超时
			},
		}
	}
	if common.RelayTimeoutV == 0 {
		VhttpClient = &http.Client{}
	} else {
		VhttpClient = &http.Client{
			Timeout: time.Duration(common.RelayTimeoutV) * time.Second,
		}
	}

	impatientHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
	}
}

func GetHttpClient() *http.Client {
	return httpClient
}

func GetStreamHttpClient() *http.Client {
	return streamhttpClient
}

func GetImpatientHttpClient() *http.Client {
	return impatientHTTPClient
}

func GetVHttpClient() *http.Client {
	return VhttpClient
}
