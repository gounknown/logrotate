Testing "write: No space left on device" error.

1. Build the Docker image:
```bash
sudo docker build -t test-image .

```
2. Run a Docker container:
```bash
sudo docker run --rm -d --privileged test-image

```
3. Enter a running container:
```bash
sudo docker exec -it <container-name-or-id> /bin/sh

```
4. Run `main` in dir `/ramdisk`:
```bash
cd /ramdisk
./main
```