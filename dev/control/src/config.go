package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
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
	AllowedHosts                                   []string
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func configInit() (config, error) {
	c := config{Listen: value("CONTROL_LISTEN", "127.0.0.1:13021"), Data: value("CONTROL_DATA", "/data"), Endpoint: os.Getenv("RUSTFS_ENDPOINT"), AK: os.Getenv("RUSTFS_ACCESS_KEY"), SK: os.Getenv("RUSTFS_SECRET_KEY"), Bucket: os.Getenv("RUSTFS_BUCKET_NAME"), Region: value("RUSTFS_REGION", "us-east-1")}
	for _, host := range strings.Split(os.Getenv("CONTROL_ALLOWED_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			c.AllowedHosts = append(c.AllowedHosts, host)
		}
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(value("CONTROL_RUNTIMES", "runtime1,runtime2"), ",") {
		name = strings.TrimSpace(name)
		if !regexp.MustCompile(`^runtime[1-9][0-9]*$`).MatchString(name) || seen[name] {
			return c, fmt.Errorf("CONTROL_RUNTIMES requires unique runtimeN names")
		}
		seen[name] = true
		prefix := strings.ToUpper(name)
		api, admin, proxy, web := "", "", "", ""
		switch name {
		case "runtime1":
			api, admin, proxy, web = "http://127.0.0.1:14010", "http://127.0.0.1:14012", "http://127.0.0.1:14013", "http://127.0.0.1:13001"
		case "runtime2":
			api, admin, proxy, web = "http://127.0.0.1:14020", "http://127.0.0.1:14022", "http://127.0.0.1:14023", "http://localhost:13011"
		}
		rt := runtimeConfig{Name: name, API: value(prefix+"_API_URL", api), Admin: value(prefix+"_ADMIN_URL", admin), Proxy: value(prefix+"_PROXY_URL", proxy), Web: value(prefix+"_WEB_URL", web), Key: os.Getenv(prefix + "_API_KEY")}
		if rt.API == "" || rt.Admin == "" || rt.Proxy == "" || rt.Web == "" {
			return c, fmt.Errorf("%s URLs required", name)
		}
		c.Runtimes = append(c.Runtimes, rt)
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
