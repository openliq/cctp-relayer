package config

import (
	"encoding/json"
	"fmt"
	"github.com/openliq/cctp-relayer/internal/constant"
	"os"
	"path/filepath"
)

const DefaultConfigPath = "./config.json"

type Config struct {
	Chains []RawChainConfig `json:"chains"`
	Other  Construction     `json:"other,omitempty"`
}

type RawChainConfig struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	Type     string `json:"type"`
	Id       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Opts     opt    `json:"opts"`
}

type opt struct {
	Mcs                string `json:"mcs,omitempty"`
	Usdc               string `json:"usdc,omitempty"`
	Event              string `json:"event,omitempty"`
	GasLimit           string `json:"gasLimit,omitempty"`
	StartBlock         string `json:"startBlock,omitempty"`
	MaxGasPrice        string `json:"maxGasPrice,omitempty"`
	GasMultiplier      string `json:"gasMultiplier,omitempty"`
	LimitMultiplier    string `json:"limitMultiplier,omitempty"`
	BlockConfirmations string `json:"blockConfirmations,omitempty"`
}

type Construction struct {
	MonitorUrl     string `json:"monitor_url,omitempty"`
	AttestationUrl string `json:"attestation_url,omitempty"`
	Env            string `json:"env,omitempty"`
}

func Local(cfgFile string) (*Config, error) {
	var fig Config
	path := DefaultConfigPath
	if cfgFile != "" {
		path = cfgFile
	}

	err := loadConfig(path, &fig)
	if err != nil {
		return &fig, err
	}

	err = fig.validate()
	if err != nil {
		return nil, err
	}
	return &fig, nil
}

func loadConfig(file string, config *Config) error {
	ext := filepath.Ext(file)
	fp, err := filepath.Abs(file)
	if err != nil {
		return err
	}

	f, err := os.Open(filepath.Clean(fp))
	if err != nil {
		return err
	}

	if ext == ".json" {
		if err = json.NewDecoder(f).Decode(&config); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unrecognized extention: %s", ext)
	}

	return nil
}

func (c *Config) validate() error {
	for idx, chain := range c.Chains {
		if chain.Id == "" {
			return fmt.Errorf("required field chain.Id empty for chain %s", chain.Id)
		}
		if chain.Type == "" {
			c.Chains[idx].Type = constant.Ethereum
		}
		if chain.Endpoint == "" {
			return fmt.Errorf("required field chain.Endpoint empty for chain %s", chain.Id)
		}
		if chain.Name == "" {
			return fmt.Errorf("required field chain.Name empty for chain %s", chain.Id)
		}
	}
	return nil
}
