.PHONY: build

ORA_VERSION := 12.2

build:
	docker build -t goora ${ORA_VERSION}/golang1.10stretch

startdb:
	docker run -d -it -p 1521:1521 -p 5500:5500  --name oracledb container-registry.oracle.com/database/enterprise:12.2.0.1

run: build
	export DATA_SOURCE_NAME=C##demouser/hemligt@dockerhost:1521/ORCLCDB.localdomain
	export ENV NLS_LANG=AMERICAN_AMERICA.UTF8
	docker run -it --rm --link oracledb:oracledb -p 9161:9161 goora