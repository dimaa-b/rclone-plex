// Package api contains definitions for using the premiumize.me API
package debrid

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
)

var baseUrl string = "https://api.real-debrid.com/rest/1.0"

func AddMagnet(magnet string, authToken string) (id string, err error) {
	// Construct the form data
	formData := url.Values{}
	formData.Set("magnet", magnet)

	client := &http.Client{}
	req, err := http.NewRequest("POST", baseUrl+"/torrents/addMagnet", bytes.NewBufferString(formData.Encode()))
	if err != nil {
		fmt.Println("Error creating HTTP request:", err)
		return "", fmt.Errorf("Error creating HTTP request:", err)
	}
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending HTTP request:", err)
		return "", fmt.Errorf("Error sending HTTP request: %s", err)
	}
	defer resp.Body.Close()

	// Check if request was
	// handle error properly

	responseBody, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		fmt.Println("Error reading response body:", err)
	}

	// Parse the response into a Response struct
	var response AddMagnetResponse
	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		fmt.Println("Error parsing response:", err)
		return "", fmt.Errorf("Error parsing response")
	}

	return response.ID, nil
}

// start torrent
// check instant avaliablity
// get links function

func StartTorrent(torrentId string, authToken string) (err error) {
	client := &http.Client{}
	formData := url.Values{}
	formData.Set("files", "all")
	req, err := http.NewRequest("POST", baseUrl+"/torrents/selectFiles/"+torrentId, bytes.NewBufferString(formData.Encode()))
	if err != nil {
		fmt.Println("Error creating HTTP request:", err)
		return err
	}
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending HTTP request:", err)
		return err
	}
	if resp.StatusCode != 204 {
		return errors.New("an error has occurred")
	}

	defer resp.Body.Close()
	return nil

}

// TO-DO return object instead of just links value (debrid functions)
// TO-DO Split debrid functions into files
// TO-DO proper error handling
// TO-DO check if torrent is already avaliable sp it doesn't add it again
func GetTorrentInfo(torrentId string, authToken string) (res TorrentInfoResponse, err error) {
	var response TorrentInfoResponse
	client := &http.Client{}
	req, err := http.NewRequest("GET", baseUrl+"/torrents/info/"+torrentId, nil)
	if err != nil {
		fmt.Println("Error creating HTTP request:", err)
		return response, err
	}
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending HTTP request:", err)
		return response, err
	}
	defer resp.Body.Close()

	// Check if request was
	// handle error properly

	responseBody, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		fmt.Println("Error reading response body:", err)
		return response, err
	}

	// Parse the response into a Response struct
	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		fmt.Println("Error parsing response:", err)
		return response, err
	}
	// DEBUG
	//fmt.Println("Response Body:", string(responseBody))
	//fmt.Println(response.Links)

	return response, nil
}

func UnrestrictLink(link string, authToken string) (generatedLink string, err error) {
	//fmt.Println("Unrestricting link")
	formData := url.Values{}
	formData.Set("link", link)

	client := &http.Client{}
	req, err := http.NewRequest("POST", baseUrl+"/unrestrict/link", bytes.NewBufferString(formData.Encode()))
	if err != nil {
		fmt.Println("Error creating HTTP request:", err)
		return "", err
	}
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending HTTP request:", err)
		return
	}
	defer resp.Body.Close()

	// Check if request was
	// handle error properly

	responseBody, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		fmt.Println("Error reading response body:", err)
		return "", err
	}

	// Parse the response into a Response struct
	var response UnrestrictFileResponse
	err = json.Unmarshal(responseBody, &response)

	if err != nil {
		fmt.Println("Error parsing response:", err)
		return "", err
	}
	// DEBUG
	//fmt.Println("Response Body:", string(responseBody))
	//fmt.Println(response.Links)

	return response.Download, nil
}

/*
Combines all previous functions into one call from the main package
*/
func AddToDebrid(magnet string, authToken string) (res TorrentInfoResponse, success bool) {
	var torrentID, _ = AddMagnet(magnet, authToken)
	if torrentID == "" {
		fmt.Println("Error occured during debrid.AddMagnet")
		return res, false
	}
	err := StartTorrent(torrentID, authToken)
	if err != nil {
		return res, false
	}

	var torrentInfo, torrentInfoErr = GetTorrentInfo(torrentID, authToken)
	if torrentInfoErr != nil {
		fmt.Println("Error occured during debrid.GetTorrentInfo")
		return res, false
	}

	return torrentInfo, true
}

type AddMagnetResponse struct {
	ID  string `json:"id"`
	URI string `json:"uri"`
}

type TorrentInfoResponse struct {
	ID               string        `json:"id"`
	Filename         string        `json:"filename"`
	OriginalFilename string        `json:"original_filename"`
	Hash             string        `json:"hash"`
	Bytes            int           `json:"bytes"`
	OriginalBytes    int           `json:"original_bytes"`
	Host             string        `json:"host"`
	Split            int           `json:"split"`
	Progress         int           `json:"progress"`
	Status           string        `json:"status"`
	Added            string        `json:"added"`
	Files            []TorrentFile `json:"files"`
	Links            []string      `json:"links"`
	Ended            string        `json:"ended,omitempty"`
	Speed            int           `json:"speed,omitempty"`
	Seeders          int           `json:"seeders,omitempty"`
}

type TorrentFile struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

type UnrestrictFileResponse struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mimeType"`
	FileSize   int    `json:"filesize"`
	Link       string `json:"link"`
	Host       string `json:"host"`
	Chunks     int    `json:"chunks"`
	CRC        int    `json:"crc"`
	Download   string `json:"download"`
	Streamable int    `json:"streamable"`
}
