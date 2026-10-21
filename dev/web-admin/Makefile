.DEFAULT_GOAL := help
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
.PHONY: help deploy
help:
	@echo 'deploy  Build and install Runtime Web Admin on the two development VMs'
deploy:
	bash "$(ROOT)/scripts/web-deploy.sh"
