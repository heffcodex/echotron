/*
 * Echotron
 * Copyright (C) 2018-2022 The Echotron Devs
 *
 * Echotron is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * Echotron is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package echotron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

type client struct {
	*http.Client
	cl map[string]*rate.Limiter // chat based limiter
	gl *rate.Limiter            // global limiter
}

const (
	globalRateLimit = 30
)

var lclient = newClient()

func newClient() client {
	return client{
		Client: &http.Client{},
		cl:     make(map[string]*rate.Limiter),
		gl:     rate.NewLimiter(rate.Every(time.Second/30), 10),
	}
}

func (c client) wait(chatID string) error {
	var ctx = context.Background()

	// If the chatID is empty, it's a general API call like GetUpdates, GetMe
	// and similar, so skip the per-chat request limit wait.
	if chatID != "" {
		// If no limiter exists for a chat, create one.
		l, ok := c.cl[chatID]
		if !ok {
			l = rate.NewLimiter(rate.Every(time.Minute/20), 10)
			c.cl[chatID] = l
		}

		// Make sure to respect the single chat limit of requests.
		if err := l.Wait(ctx); err != nil {
			return err
		}
	}

	// Make sure to respect the global limit of requests.
	return c.gl.Wait(ctx)
}

func (c client) doGet(reqURL string) ([]byte, error) {
	resp, err := c.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c client) doPost(reqURL string, files ...content) ([]byte, error) {
	var (
		buf = new(bytes.Buffer)
		w   = multipart.NewWriter(buf)
	)

	for _, f := range files {
		part, err := w.CreateFormFile(f.ftype, filepath.Base(f.fname))
		if err != nil {
			return nil, err
		}
		part.Write(f.fdata)
	}
	w.Close()

	req, err := http.NewRequest(http.MethodPost, reqURL, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", w.FormDataContentType())

	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func (c client) doPostForm(reqURL string, keyVals map[string]string) ([]byte, error) {
	var form = make(url.Values)

	for k, v := range keyVals {
		form.Add(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.PostForm = form
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func (c client) sendFile(file, thumbnail InputFile, url, fileType string) (res []byte, err error) {
	var cnt []content

	if file.id != "" {
		url = fmt.Sprintf("%s&%s=%s", url, fileType, file.id)
	} else if file.url != "" {
		url = fmt.Sprintf("%s&%s=%s", url, fileType, file.url)
	} else if c, e := toContent(fileType, file); e == nil {
		cnt = append(cnt, c)
	} else {
		err = e
	}

	if c, e := toContent("thumbnail", thumbnail); e == nil {
		cnt = append(cnt, c)
	} else {
		err = e
	}

	if len(cnt) > 0 {
		res, err = c.doPost(url, cnt...)
	} else {
		res, err = c.doGet(url)
	}
	return
}

func (c client) get(base, endpoint string, vals url.Values, v APIResponse) error {
	url, err := url.JoinPath(base, endpoint)
	if err != nil {
		return err
	}

	if vals != nil {
		if queries := vals.Encode(); queries != "" {
			url = fmt.Sprintf("%s?%s", url, queries)
		}
	}

	if err := c.wait(vals.Get("chat_id")); err != nil {
		return err
	}

	cnt, err := c.doGet(url)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(cnt, v); err != nil {
		return err
	}
	return check(v)
}

func (c client) postFile(base, endpoint, fileType string, file, thumbnail InputFile, vals url.Values, v APIResponse) error {
	url, err := joinURL(base, endpoint, vals)
	if err != nil {
		return err
	}

	if err := c.wait(vals.Get("chat_id")); err != nil {
		return err
	}

	cnt, err := c.sendFile(file, thumbnail, url, fileType)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(cnt, v); err != nil {
		return err
	}
	return check(v)
}

func (c client) postMedia(base, endpoint string, editSingle bool, vals url.Values, v APIResponse, files ...InputMedia) error {
	url, err := joinURL(base, endpoint, vals)
	if err != nil {
		return err
	}

	if err := c.wait(vals.Get("chat_id")); err != nil {
		return err
	}

	cnt, err := c.sendMediaFiles(url, editSingle, files...)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(cnt, v); err != nil {
		return err
	}
	return check(v)
}

func (c client) postStickers(base, endpoint string, vals url.Values, v APIResponse, stickers ...InputSticker) error {
	url, err := joinURL(base, endpoint, vals)
	if err != nil {
		return err
	}

	if err := c.wait(vals.Get("chat_id")); err != nil {
		return err
	}

	cnt, err := c.sendStickers(url, stickers...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(cnt, v); err != nil {
		return err
	}
	return check(v)
}

func (c client) sendMediaFiles(url string, editSingle bool, files ...InputMedia) (res []byte, err error) {
	var (
		med []mediaEnvelope
		cnt []content
		jsn []byte
	)

	for _, file := range files {
		var im mediaEnvelope
		var cntArr []content

		media := file.media()
		thumbnail := file.thumbnail()

		im, cntArr, err = processMedia(media, thumbnail)
		if err != nil {
			return
		}

		im.InputMedia = file

		med = append(med, im)
		cnt = append(cnt, cntArr...)
	}

	if editSingle {
		jsn, err = json.Marshal(med[0])
	} else {
		jsn, err = json.Marshal(med)
	}

	if err != nil {
		return
	}

	url = fmt.Sprintf("%s&media=%s", url, jsn)

	if len(cnt) > 0 {
		return c.doPost(url, cnt...)
	}
	return c.doGet(url)
}

func (c client) sendStickers(url string, stickers ...InputSticker) (res []byte, err error) {
	var (
		sti []stickerEnvelope
		cnt []content
		jsn []byte
	)

	for _, s := range stickers {
		var se stickerEnvelope
		var cntArr []content

		se, cntArr, err = processSticker(s.Sticker)
		if err != nil {
			return
		}

		se.InputSticker = s

		sti = append(sti, se)
		cnt = append(cnt, cntArr...)
	}

	if len(sti) == 1 {
		jsn, _ = json.Marshal(sti[0])
		url = fmt.Sprintf("%s&sticker=%s", url, jsn)
	} else {
		jsn, _ = json.Marshal(sti)
		url = fmt.Sprintf("%s&stickers=%s", url, jsn)
	}

	if len(cnt) > 0 {
		return c.doPost(url, cnt...)
	}
	return c.doGet(url)
}

func isGroup(chatID string) bool {
	return strings.HasPrefix(chatID, "-")
}
