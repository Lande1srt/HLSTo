package service

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

type TsInfo struct {
	Name string
	Url  string
}

func (ds *DownloaderService) getHost(Url string, ht string) string {
	u, err := url.Parse(Url)
	if err != nil {
		return ""
	}
	switch ht {
	case "v1":
		return u.Scheme + "://" + u.Host + path.Dir(u.EscapedPath())
	case "v2":
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host
}

func (ds *DownloaderService) getM3u8Body(Url string, referer string, cookie string) string {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)

	// 使用自定义 http.Client 以便在重定向时保留 Referer 和 Cookie
	client := &http.Client{
		Timeout: HEAD_TIMEOUT,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// 关键：在重定向请求中手动补回 Referer, Origin 和 Cookie
			if referer != "" {
				req.Header.Set("Referer", referer)
				if u, err := url.Parse(referer); err == nil {
					req.Header.Set("Origin", u.Scheme+"://"+u.Host)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
			return nil
		},
	}

	req, err := http.NewRequest("GET", Url, nil)
	if err != nil {
		return ""
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	if referer != "" {
		req.Header.Set("Referer", referer)
		if u, err := url.Parse(referer); err == nil {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return string(body)
}

func (ds *DownloaderService) getM3u8Key(host, html string, referer string, cookie string) string {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)
	lines := strings.Split(html, "\n")
	for _, line := range lines {
		if strings.Contains(line, "#EXT-X-KEY") {
			if !strings.Contains(line, "URI") {
				continue
			}
			uriPos := strings.Index(line, "URI")
			quotationMarkPos := strings.LastIndex(line, "\"")
			keyUrl := strings.Split(line[uriPos:quotationMarkPos], "\"")[1]
			if !strings.Contains(line, "http") {
				keyUrl = fmt.Sprintf("%s/%s", host, keyUrl)
			}

			// 使用自定义 http.Client 以便在重定向时保留 Referer 和 Cookie
			client := &http.Client{
				Timeout: HEAD_TIMEOUT,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 10 {
						return fmt.Errorf("too many redirects")
					}
					if referer != "" {
						req.Header.Set("Referer", referer)
						if u, err := url.Parse(referer); err == nil {
							req.Header.Set("Origin", u.Scheme+"://"+u.Host)
						}
					}
					if cookie != "" {
						req.Header.Set("Cookie", cookie)
					}
					req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
					return nil
				},
			}

			req, err := http.NewRequest("GET", keyUrl, nil)
			if err != nil {
				continue
			}

			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, Gecko) Chrome/120.0.0.0 Safari/537.36")
			req.Header.Set("Connection", "keep-alive")
			req.Header.Set("Accept", "*/*")

			if referer != "" {
				req.Header.Set("Referer", referer)
				if u, err := url.Parse(referer); err == nil {
					req.Header.Set("Origin", u.Scheme+"://"+u.Host)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}

			res, err := client.Do(req)
			if err != nil || res.StatusCode != 200 {
				continue
			}
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				continue
			}
			return string(body)
		}
	}
	return ""
}

func (ds *DownloaderService) getTsList(host, body string) []TsInfo {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var tsList []TsInfo
	index := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 只要不是以 # 开头的行，都视为分片地址（兼容 .jpeg, .png 等伪装后缀）
		if !strings.HasPrefix(line, "#") {
			index++
			fullUrl := ""
			if strings.HasPrefix(line, "http") {
				fullUrl = line
			} else if strings.HasPrefix(line, "//") {
				// 处理协议相对路径
				fullUrl = "https:" + line
			} else {
				// 处理相对路径
				line = strings.TrimPrefix(line, "/")
				fullUrl = fmt.Sprintf("%s/%s", host, line)
			}

			tsList = append(tsList, TsInfo{
				Name: fmt.Sprintf(TS_NAME_TEMPLATE, index),
				Url:  fullUrl,
			})
		}
	}
	return tsList
}
