# goora
* docker file for go with oracle
* using https://github.com/mattn/go-oci8
* not included oracle client package, please get these under licence of agreement of Oracle.

## from oracle download these and put in ${version}/${variant}/package
Or the version you wish to use. This is for 12.2.0.1
 * oracle-instantclient12.2-basic-12.2.0.1.0-1.x86_64.rpm
 * oracle-instantclient12.2-devel-12.2.0.1.0-1.x86_64.rpm
 * oracle-instantclient12.2-sqlplus-12.2.0.1.0-1.x86_64.rpm


## usage
* set oracle client package rpm under ${version}/${variant}/package dir.
  * ex. http://www.oracle.com/technetwork/topics/linuxx86-64soft-092277.html?ssSourceSiteId=otnjp
* `make` 
* run docker
```
docker run -it --rm goora
```

## CONTRIBUTE
* Welcome to contribute, make issue or pr.

## LICENSE
* MIT, see LICENSE
