#!/bin/sh
RAPSERVER=$1
shift
go run main.go --printurl $RAPSERVER | xargs -L 1 wrk -c 128 $*
