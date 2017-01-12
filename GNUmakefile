VERSION = $(shell git describe --tags)
BUILD_TMP?=.rpm_tmp

include common.mk

.PHONY: deb
deb:
	rm -rf ${BUILD_TMP}
	mkdir -p ${BUILD_TMP}/usr/local/bin/
	mkdir -p ${BUILD_TMP}/var/log/goircd
	mkdir -p ${BUILD_TMP}/etc/systemd/system/
	cp goircd ${BUILD_TMP}/usr/local/bin/
	cp startup/goircd.service ${BUILD_TMP}/etc/systemd/system/
	fpm -s dir -t deb -n goircd -v ${VERSION}\
        -m mengzhuo1203@gmail.com \
        --deb-compression=bzip2 \
        --verbose \
        -d logrotate\
        -C ${BUILD_TMP}

