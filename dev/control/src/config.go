package main

import (
	"fmt"
	"os"
)

type runtimeConfig struct {
	Name  string `json:"name"`
	Proxy string `json:"-"`
	API   string `json:"-"`
	Admin string `json:"-"`
	Key   string `json:"-"`
	Web   string `json:"web_url"`
}
type config struct {
	Listen, Data, Endpoint, AK, SK, Bucket, Region string
	Runtimes                                       []runtimeConfig
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func configInit() (config, error) {
	c := config{Listen: value("CONTROL_LISTEN", "127.0.0.1:13021"), Data: value("CONTROL_DATA", "/data"), Endpoint: os.Getenv("RUSTFS_ENDPOINT"), AK: os.Getenv("RUSTFS_ACCESS_KEY"), SK: os.Getenv("RUSTFS_SECRET_KEY"), Bucket: os.Getenv("RUSTFS_BUCKET_NAME"), Region: value("RUSTFS_REGION", "us-east-1")}
	// Runtime credentials are server-only. Examples are local SSH forwards on 211.
	for _, r := range []struct{ name, prefix, api, admin, web string }{
		{"runtime1", "RUNTIME1", "http://127.0.0.1:14010", "http://127.0.0.1:14012", "http://127.0.0.1:13001"},
		{"runtime2", "RUNTIME2", "http://127.0.0.1:14020", "http://127.0.0.1:14022", "http://localhost:13011"},
	} {
		c.Runtimes = append(c.Runtimes, runtimeConfig{Name: r.name, API: value(r.prefix+"_API_URL", r.api), Admin: value(r.prefix+"_ADMIN_URL", r.admin), Key: os.Getenv(r.prefix + "_API_KEY"), Web: value(r.prefix+"_WEB_URL", r.web), Proxy: value(r.prefix+"_PROXY_URL", map[string]string{"runtime1": "http://127.0.0.1:14013", "runtime2": "http://127.0.0.1:14023"}[r.name])})
	}
	if c.AK == "" || c.SK == "" || c.Endpoint == "" || c.Bucket == "" {
		return c, fmt.Errorf("S3 configuration required")
	}
	for _, r := range c.Runtimes {
		if r.Key == "" {
			return c, fmt.Errorf("%s API key required", r.Name)
		}
	}
	return c, nil
}
