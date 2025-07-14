# Docker Container File Copy Errors: Quoting, Globs, and Go

## Problem
When using Go to automate file copy operations inside a Docker container (with `bash -c`), copying files using a glob pattern like:

```sh
docker exec <container> bash -c "cp -r /src/* /dst"
```

or in Go:
```go
exec.Command("docker", "exec", containerName, "bash", "-c", "cp -r /src/* /dst")
```

can fail with errors like:

```
cp: cannot stat '/src/somefile': No such file or directory
```

This is due to shell quoting and glob expansion issues. Go's `exec.Command` passes the command as a single string, and `bash -c` expects the command as one argument. Globs may not be expanded as expected in this context, especially if extra quotes are added or if the shell environment is not fully initialized.

## Solution
Use this alternative form to copy all files (including hidden ones) reliably:

```sh
cp -r /src/. /dst
```

So in Go:
```go
copyCmd := fmt.Sprintf("cp -r %s/. %s", src, dst)
exec.Command("docker", "exec", containerName, "bash", "-c", copyCmd)
```

- Do **not** add extra quotes around the command string in Go.
- `/src/.` will copy all files and folders (including dotfiles) from `/src` to `/dst` without relying on shell glob expansion.

## References
- [GNU cp documentation](https://www.gnu.org/software/coreutils/manual/html_node/cp-invocation.html)
- [Docker exec and shell quoting issues](https://github.com/moby/moby/issues/22260)

## Debugging Tips
- If you need to debug further, leave the container running after a failure so you can inspect its state interactively:
  ```sh
  docker exec -it <container> bash
  ```
- Always check for quoting issues and avoid unnecessary shell globs in automation scripts.

---

**Summary:**
> Use `cp -r /src/. /dst` instead of `cp -r /src/* /dst` when copying files inside a Docker container via Go/bash automation to avoid quoting and glob expansion errors.
