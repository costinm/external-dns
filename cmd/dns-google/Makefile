
OUT := ${GOPATH}/bin

push:
	go build -o ${OUT}/dns-google .
	cd ${OUT} && tar cf - dns-google | gcrane load -i ${HUB}/dns-google:latest
