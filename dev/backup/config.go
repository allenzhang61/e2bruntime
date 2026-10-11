package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	API, Proxy, Key, Storage, Node, Endpoint, AK, SK, Bucket, Region string
	Interval                                                         time.Duration
	Keep                                                             int
	ImportSources, ImportJobs                                        string
}

func configInit() (config, error) {
	c := config{
		ImportSources: env("BACKUP_IMPORT_SOURCES", "runtime1,runtime2"),
		ImportJobs:    env("BACKUP_IMPORT_JOBS", "/opt/e2b-runtime/backup/import-jobs"),
		API:           env("BACKUP_API_URL", "http://127.0.0.1:3000"),
		Proxy:         env("BACKUP_PROXY_URL", "http://127.0.0.1:3002"),
		Key:           os.Getenv("E2B_API_KEY"), // Loaded from sdk.env; never log.
		Storage:       env("BACKUP_STORAGE_ROOT", "/var/lib/e2b/storage/templates"),
		Node:          os.Getenv("BACKUP_NODE"),        // Example: runtime1 or runtime2.
		Endpoint:      os.Getenv("RUSTFS_ENDPOINT"),    // Example: http://192.168.1.211:19000.
		AK:            os.Getenv("RUSTFS_ACCESS_KEY"),  // Loaded from s3.env; never log.
		SK:            os.Getenv("RUSTFS_SECRET_KEY"),  // Loaded from s3.env; never log.
		Bucket:        os.Getenv("RUSTFS_BUCKET_NAME"), // Example: runtime-snapshots.
		Region:        env("RUSTFS_REGION", "us-east-1"),
	}
	var e error
	c.Interval, e = time.ParseDuration(env("BACKUP_INTERVAL", "2m"))
	if e != nil || c.Interval < time.Second {
		return c, fmt.Errorf("invalid BACKUP_INTERVAL")
	}
	c.Keep, e = strconv.Atoi(env("BACKUP_KEEP", "10"))
	if e != nil || c.Keep < 1 {
		return c, fmt.Errorf("invalid BACKUP_KEEP")
	}
	if c.Key == "" || c.Endpoint == "" || c.AK == "" || c.SK == "" || c.Bucket == "" || c.Node == "" || strings.ContainsAny(c.Node, "/\\.") {
		return c, fmt.Errorf("API key, S3 configuration and safe BACKUP_NODE required")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
