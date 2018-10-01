.PHONY: build

ORA_VERSION := 12.2

build:
	docker-compose build --no-cache

startdb:
	docker-compose up -d oracledb

run: 
	docker-compose up -d oracle-collector metrics-gateway prometheus-server grafana-ui