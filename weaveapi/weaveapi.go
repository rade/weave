package weaveapi

import (
	"errors"
	"fmt"
	"github.com/zettio/weave/router"
	"io/ioutil"
	"net/http"
	"net/url"
)

type Client struct {
	baseUrl string
}

func httpGet(url string) (string, error) {
	if resp, err := http.Get(url); err != nil {
		return "", err
	} else {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return "", errors.New(resp.Status + ": " + string(body))
		} else {
			return string(body), nil
		}
	}
}

func httpPost(url string, values url.Values) (string, error) {
	fmt.Println("Url", url, "Values", values)
	if resp, err := http.PostForm(url, values); err != nil {
		return "", err
	} else if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	} else {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)
		return string(body), nil
	}
}

func NewClient(addr string) *Client {
	return &Client{baseUrl: fmt.Sprintf("http://%s:%d", addr, router.HttpPort)}
}

func (client *Client) Connect(remote string) error {
	_, err := httpGet(client.baseUrl + "/connect?peer=" + remote)
	return err
}

func (client *Client) AllocateIPFor(id string) (string, error) {
	ret, err := httpGet(client.baseUrl + "/ip/" + id)
	return ret, err
}
