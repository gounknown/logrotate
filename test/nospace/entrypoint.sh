#!/bin/sh

mkdir /ramdisk
mount -t tmpfs -o size=10m tmpfs /ramdisk

cp /app/main /ramdisk/
chmod +x /ramdisk/main

cd /ramdisk

while true; do sleep 30; done;