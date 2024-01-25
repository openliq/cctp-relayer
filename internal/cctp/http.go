package cctp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Cctper interface {
	GetProof(hash string) (*Resp, error)
}

type cctp struct {
	endpoint string
}

func New(url string) Cctper {
	return &cctp{endpoint: url}
}

func (c *cctp) GetProof(hash string) (*Resp, error) {
	url := fmt.Sprintf(c.endpoint + "/" + hash)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	ret := &Resp{}
	err = json.Unmarshal(body, ret)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

type Resp struct {
	Attestation string `json:"attestation"`
	Status      string `json:"status"`
}
