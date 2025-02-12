package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"one-api/common"
	"one-api/dto"
	"one-api/setting"
	"strings"

	"golang.org/x/image/webp"
)

func DecodeBase64ImageData(base64String string) (image.Config, string, string, error) {
	// 去除base64数据的URL前缀（如果有）
	if idx := strings.Index(base64String, ","); idx != -1 {
		base64String = base64String[idx+1:]
	}

	// 将base64字符串解码为字节切片
	decodedData, err := base64.StdEncoding.DecodeString(base64String)
	if err != nil {
		fmt.Println("Error: Failed to decode base64 string")
		return image.Config{}, "", "", err
	}

	// 创建一个bytes.Buffer用于存储解码后的数据
	reader := bytes.NewReader(decodedData)
	config, format, err := getImageConfig(reader)
	return config, format, base64String, err
}

func DecodeBase64FileData(base64String string) (string, string, error) {
	var mimeType string
	var idx int
	idx = strings.Index(base64String, ",")
	if idx == -1 {
		_, file_type, base64, err := DecodeBase64ImageData(base64String)
		return "image/" + file_type, base64, err
	}
	mimeType = base64String[:idx]
	base64String = base64String[idx+1:]
	idx = strings.Index(mimeType, ";")
	if idx == -1 {
		_, file_type, base64, err := DecodeBase64ImageData(base64String)
		return "image/" + file_type, base64, err
	}
	mimeType = mimeType[:idx]
	idx = strings.Index(mimeType, ":")
	if idx == -1 {
		_, file_type, base64, err := DecodeBase64ImageData(base64String)
		return "image/" + file_type, base64, err
	}
	mimeType = mimeType[idx+1:]
	return mimeType, base64String, nil
}

// GetImageFromUrl 获取图片的类型和base64编码的数据
func GetImageFromUrl(url string) (mimeType string, data string, err error) {
	resp, err := DoDownloadRequest(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	// 从响应头获取 MIME 类型
	mimeType = resp.Header.Get("Content-Type")

	// 如果无效或不是图片，改用部分数据检测
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		buf := make([]byte, 512)
		n, err := io.ReadFull(resp.Body, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return "", "", err
		}
		detected := http.DetectContentType(buf[:n])
		if !strings.HasPrefix(detected, "image/") {
			return "", "", fmt.Errorf("invalid content type: %s, required image/*", detected)
		}
		mimeType = detected
		body := io.MultiReader(bytes.NewReader(buf[:n]), resp.Body)
		allData, err := io.ReadAll(body)
		if err != nil {
			return "", "", err
		}
		data = base64.StdEncoding.EncodeToString(allData)
		return mimeType, data, nil
	}

	// 如果头部类型有效，直接读取全部内容
	allData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	data = base64.StdEncoding.EncodeToString(allData)
	return mimeType, data, nil
}

func DecodeUrlImageData(imageUrl string) (image.Config, string, error) {
	response, err := DoDownloadRequest(imageUrl)
	if err != nil {
		common.SysLog(fmt.Sprintf("fail to get image from url: %s", err.Error()))
		return image.Config{}, "", err
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		err = errors.New(fmt.Sprintf("fail to get image from url: %s", response.Status))
		return image.Config{}, "", err
	}

	// 首先检查响应头的 Content-Type
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		if !strings.HasPrefix(contentType, "image/") {
			common.SysLog(fmt.Sprintf("invalid content type from header: %s", contentType))
		} else {
			// Content-Type 验证通过,继续处理
			goto ProcessImage
		}
	}

	// 如果响应头判断失败,读取部分数据判断MIME类型
	{
		buf := make([]byte, 512)
		n, err := io.ReadFull(response.Body, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return image.Config{}, "", err
		}
		mimeType := http.DetectContentType(buf[:n])
		if !strings.HasPrefix(mimeType, "image/") {
			return image.Config{}, "", fmt.Errorf("invalid content type: %s, required image/*", mimeType)
		}
		// 复原数据流
		response.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf[:n]), response.Body))
	}

ProcessImage:
	var readData []byte
	for _, limit := range []int64{1024 * 8, 1024 * 24, 1024 * 64} {
		common.SysLog(fmt.Sprintf("try to decode image config with limit: %d", limit))

		// 从response.Body读取更多的数据直到达到当前的限制
		additionalData := make([]byte, limit-int64(len(readData)))
		n, _ := io.ReadFull(response.Body, additionalData)
		readData = append(readData, additionalData[:n]...)

		// 使用io.MultiReader组合已经读取的数据和response.Body
		limitReader := io.MultiReader(bytes.NewReader(readData), response.Body)

		var config image.Config
		var format string
		config, format, err = getImageConfig(limitReader)
		if err == nil {
			return config, format, nil
		}
	}

	return image.Config{}, "", err // 返回最后一个错误
}

func getImageConfig(reader io.Reader) (image.Config, string, error) {
	// 读取图片的头部信息来获取图片尺寸
	config, format, err := image.DecodeConfig(reader)
	if err != nil {
		err = errors.New(fmt.Sprintf("fail to decode image config(gif, jpg, png): %s", err.Error()))
		common.SysLog(err.Error())
		config, err = webp.DecodeConfig(reader)
		if err != nil {
			err = errors.New(fmt.Sprintf("fail to decode image config(webp): %s", err.Error()))
			common.SysLog(err.Error())
		}
		format = "webp"
	}
	if err != nil {
		return image.Config{}, "", err
	}
	return config, format, nil
}

func ImageDomainWhitelistCheck(url string) bool {
	if len(setting.ImageDomainWhitelist) == 0 {
		return false
	}
	url = strings.ToLower(url)
	for _, domain := range setting.ImageDomainWhitelist {
		if strings.Contains(url, domain) {
			return true
		}
	}
	return false
}

func ConvertImageUrlsToBase64(m *dto.Message, userId int) {
	contentList := m.ParseContent()
	for i, cItem := range contentList {
		if cItem.Type == dto.ContentTypeImageURL {
			if urlValue, ok := cItem.ImageUrl.(dto.MessageImageUrl); ok {
				if !strings.HasPrefix(urlValue.Url, "data:") && !strings.Contains(urlValue.Url, "aliyuncs.com") && !strings.Contains(urlValue.Url, "windows.net") &&
					(strings.HasPrefix(urlValue.Url, "http://") || strings.HasPrefix(urlValue.Url, "https://")) {
					common.SysLog(fmt.Sprintf("convert image url to base64: %s from user: %d", urlValue.Url, userId))
					mimeType, base64Data, err := GetImageFromUrl(urlValue.Url)
					if err == nil && base64Data != "" {
						urlValue.Url = fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)
						contentList[i].ImageUrl = urlValue
					}
				}
			}
		}
	}
	newContentBytes, _ := json.Marshal(contentList)
	m.Content = newContentBytes
}
