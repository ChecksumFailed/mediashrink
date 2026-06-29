package main

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"syscall"

	"golang.org/x/term"
)

type plexSectionList struct {
	Sections []plexSection `xml:"Directory"`
}

type plexSection struct {
	Key   string `xml:"key,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

type plexItemList struct {
	Videos []plexVideo `xml:"Video"`
}

type plexVideo struct {
	Media []plexMedia `xml:"Media"`
}

type plexMedia struct {
	VideoCodec string     `xml:"videoCodec,attr"`
	Parts      []plexPart `xml:"Part"`
}

type plexPart struct {
	File string `xml:"file,attr"`
	Size int64  `xml:"size,attr"`
}

type plexSignIn struct {
	Token string `xml:"authToken,attr"`
}

// PlexLogin prompts for Plex credentials and returns an auth token.
func PlexLogin() (string, error) {
	fmt.Print("Plex username/email: ")
	var username string
	fmt.Scanln(&username)

	fmt.Print("Plex password: ")
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}

	req, err := http.NewRequest("POST", "https://plex.tv/users/sign_in.xml", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(username, string(pw))
	req.Header.Set("X-Plex-Client-Identifier", "media-convert")
	req.Header.Set("X-Plex-Product", "media-convert")
	req.Header.Set("X-Plex-Version", "1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("signing in to plex.tv: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("invalid Plex credentials")
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("plex.tv returned HTTP %d", resp.StatusCode)
	}

	var result plexSignIn
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing plex sign-in response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("no token in plex.tv response")
	}
	return result.Token, nil
}

func plexClient(insecure bool) *http.Client {
	if !insecure {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func FindCandidatesFromPlex(baseURL, token string, minBytes int64, insecure, skipHEVC bool) ([]Candidate, error) {
	client := plexClient(insecure)
	var sections plexSectionList
	if err := plexFetch(client, baseURL, token, "/library/sections", &sections); err != nil {
		return nil, fmt.Errorf("fetching plex sections: %w", err)
	}

	var candidates []Candidate
	for _, section := range sections.Sections {
		var endpoint string
		switch section.Type {
		case "movie":
			endpoint = "/library/sections/" + section.Key + "/all"
		case "show":
			endpoint = "/library/sections/" + section.Key + "/allLeaves"
		default:
			continue
		}

		var items plexItemList
		if err := plexFetch(client, baseURL, token, endpoint, &items); err != nil {
			return nil, fmt.Errorf("fetching plex section %q: %w", section.Title, err)
		}

		for _, video := range items.Videos {
			for _, media := range video.Media {
				if skipHEVC && isHEVC(media.VideoCodec) {
					continue
				}
				for _, part := range media.Parts {
					if part.Size >= minBytes {
						candidates = append(candidates, Candidate{Path: part.File, Size: part.Size, Codec: media.VideoCodec})
					}
				}
			}
		}
	}

	return candidates, nil
}

func plexFetch(client *http.Client, baseURL, token, path string, out interface{}) error {
	url := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("Accept", "application/xml")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return xml.NewDecoder(resp.Body).Decode(out)
}
