# task-sync

`task-sync` is a command-line tool for defining and executing multi-step tasks. It uses a PostgreSQL database to store task and step definitions, allowing for complex workflows with dependencies.

## Database Schema

The application relies on two primary tables: `tasks` and `steps`.

### `tasks` Table

Stores the high-level tasks.

| Column     | Type        | Description                                      |
|------------|-------------|--------------------------------------------------|
| `id`         | `SERIAL`    | Primary Key                                      |
| `name`       | `TEXT`      | The unique name of the task.                     |
| `status`     | `TEXT`      | The current status of the task (e.g., 'new').    |
| `local_path` | `TEXT`      | The local filesystem path relevant to the task.  |
| `created_at` | `TIMESTAMPTZ` | Timestamp of creation.                           |
| `updated_at` | `TIMESTAMPTZ` | Timestamp of the last update.                    |

### `steps` Table

Stores the individual steps that make up a task.

| Column     | Type        | Description                                                                 |
|------------|-------------|-----------------------------------------------------------------------------|
| `id`         | `SERIAL`    | Primary Key                                                                 |
| `task_id`    | `INTEGER`   | Foreign key to the `tasks` table.                                           |
| `title`      | `TEXT`      | A descriptive title for the step.                                           |

| `settings`   | `JSONB`     | A JSON object containing the configuration for this step's execution.       |
| `results`    | `JSONB`     | A JSON object where the results of the step execution are stored.           |
| `created_at` | `TIMESTAMPTZ` | Timestamp of creation.                                                      |
| `updated_at` | `TIMESTAMPTZ` | Timestamp of the last update.                                               |

---

## Step Types & Examples

Steps are defined by the JSON content of the `settings` column. The top-level key in the `settings` object determines the type of the step.

### 1. `file_exists`

Checks for the existence of one or more files. The step succeeds only if all specified files are found.

**Settings:**

The value for `file_exists` can be a single string for one file, or an array of strings for multiple files.

*Single File:*
```json
{
  "file_exists": "path/to/your/file1.txt"
}
```

*Multiple Files:*
```json
{
  "file_exists": [
    "path/to/your/file1.txt",
    "path/to/another/file2.go"
  ]
}
```

**Example CLI Command (Multiple Files):**

```bash
./task-sync step create --task-id 1 --title "Check for source files" --settings '{
  "file_exists": [
    "main.go",
    "go.mod"
  ]
}'
```

### 2. `docker_build`

Builds a Docker image from a Dockerfile. This step can track file changes to avoid rebuilding if no source files have been modified.

**Settings:**

```json
{
  "docker_build": {
    "context": ".",
    "image_tag": "my-app:latest",
    "tags": ["my-app:1.0.0", "my-registry/my-app:latest"],
    "files": ["main.go", "go.mod", "Dockerfile"],
    "hashes": {
      "main.go": "<hash_of_main.go>",
      "go.mod": "<hash_of_go.mod>",
      "Dockerfile": "<hash_of_Dockerfile>"
    },
    "image_id": "sha256:...",
    "params": ["--build-arg", "VERSION=1.0"],
    "depends_on": [
      { "id": 101 }
    ]
  }
}
```

- `context` (optional): The build context directory for the Docker build. Defaults to the task's `local_path`.
- `image_tag` (required): The primary tag for the new Docker image.
- `tags` (optional): A list of additional tags to apply to the image.
- `files` (optional): A list of file paths to monitor for changes. If any of these files change, the image will be rebuilt.
- `hashes` (optional): A map of file paths to their SHA256 hashes. This is used to detect changes and is updated automatically by the system.
- `image_id` (output): The ID of the built image. This field is populated automatically after a successful build.
- `params` (optional): A list of extra parameters to pass to the `docker build` command (e.g., build arguments).
- `depends_on` (optional): A list of other step IDs that must be completed successfully before this step can run.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Build App Image" --settings '{
  "docker_build": {
    "context": "./app",
    "image_tag": "my-app:1.0",
    "files": [
      "app/main.go",
      "app/go.mod",
      "app/Dockerfile"
    ],
    "params": ["--no-cache"]
  }
}'
```

### 3. `docker_run`

Runs a command in a new or existing Docker container.

**Settings:**

```json
{
  "docker_run": {
    "image_tag": "my-custom-app:latest",
    "image_id": "sha256:...",
    "command": [
      "./my_app", "--verbose"
    ],
    "container_name": "my-app-container",
    "parameters": ["-p", "8080:80", "--rm"],
    "depends_on": [
      { "id": 102 }
    ]
  }
}
```

- `image_tag` (required): The Docker image to use. This is used to find the image if `image_id` is not provided.
- `image_id` (optional): The specific image ID to use. If provided by a dependency (like a `docker_build` step), it will be used to ensure the correct image is run.
- `command` (optional): The command and arguments to execute inside the container. If not provided, the container will be started with its default command.
- `container_name` (optional): A specific name for the container. If not provided, a name will be generated automatically.
- `parameters` (optional): A list of extra parameters to pass to the `docker run` command (e.g., `["-p", "8080:80", "--rm"]`).
- `depends_on` (optional): This step will typically depend on a `docker_build` or `docker_pull` step to ensure the image is available.
- `container_id` (output): The ID of the running container. This field is populated automatically.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Run App Container" --settings '{
  "docker_run": {
    "image_tag": "my-app:1.0",
    "command": ["./my_app", "--mode", "production"],
    "parameters": ["-p", "8080:80"]
  }
}'
```

### 4. `docker_shell`

Executes one or more shell commands inside a pre-existing, running Docker container. This step locates a running container by its `image_tag` and verifies the container's image hash against a provided `image_id` to ensure it is up-to-date. It does **not** create or start containers.

**Settings:**

```json
{
  "docker_shell": {
    "docker": {
      "image_tag": "my-app:1.0",
      "image_id": "sha256:f1b3f...",
      "container_name": "my-app-container",
      "container_id": "a1b2c3..."
    },
    "command": [
      { "list_files": "ls -la /app" },
      { "run_tests": "/app/run_tests.sh" }
    ],
    "depends_on": [{ "id": 2 }]
  }
}
```

- `docker` (required): An object containing the details of the target Docker container.
  - `image_tag` (optional): The image tag to look for if the container is not found by other means.
  - `image_id` (optional): The specific image ID to look for. This is often inherited from a `docker_build` dependency.
  - `container_name` (optional): The name of the container to execute the command in.
  - `container_id` (optional): The ID of the container. This is the most reliable way to target a container and is often inherited from a `docker_run` step.
- `command` (required): A list of command objects to execute sequentially. Each object is a map with a single key that serves as a label for the command (e.g., `"list_files"`, `"run_tests"`) and the shell command as its value.
- `depends_on` (optional): This step typically depends on a `docker_run` step to ensure the target container is running.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Run Tests in Container" --settings '{
  "docker_shell": {
    "command": [
      { "run_application_tests": "/app/tests/run.sh" }
    ],
    "depends_on": [{ "id": 3 }]
  }
}'
```

### 5. `docker_rubrics`

Monitors specified files for changes and runs rubric evaluations inside a Docker container. This step is useful for automated grading scenarios where submissions are evaluated against a set of rules. It combines file monitoring with execution within a containerized environment.

**Settings:**

```json
{
  "docker_rubrics": {
    "image_tag": "grader-env:v2",
    "image_id": "sha256:...",
    "files": [
      "student_submission.py",
      "tests/"
    ],
    "hashes": {
      "student_submission.py": "<hash>"
    },
    "depends_on": [{ "id": 4 }]
  }
}
```

- `image_tag` (optional): The Docker image to use for the evaluation.
- `image_id` (optional): The specific image ID to use, often inherited from a `docker_build` step.
- `files` (required): A list of files or directories to monitor for changes. If any of these change, the rubrics will be re-evaluated.
- `hashes` (output): A map of file paths to their SHA256 hashes, managed automatically by the system to track changes.
- `depends_on` (optional): This step typically depends on a `docker_run` step to ensure the container environment is ready.

**Example CLI Command:**

```bash
./task-sync step create --task-id 2 --title "Grade Submission" --settings '{
  "docker_rubrics": {
    "image_tag": "python-grader:latest",
    "files": ["submission/"],
    "depends_on": [{ "id": 9 }]
  }
}'
```

### 6. `dynamic_lab`

Monitors specified files for changes by comparing their content hashes. This step is useful for triggering other steps when a file is modified.

**Settings:**

```json
{
  "dynamic_lab": {
    "files": [
      "path/to/your/file1.txt",
      "path/to/another/file2.go"
    ],
    "hashes": {
      "path/to/your/file1.txt": "<hash_of_file1>",
      "path/to/another/file2.go": "<hash_of_file2>"
    },
    "environment": {
      "docker": false
    }
  }
}
```

- `files`: An array of file paths to monitor for changes.
- `hashes`: A map of file paths to their corresponding SHA256 hashes. This field is managed automatically by the system to track file changes. You can initialize it with empty values.
- `environment`: (Optional) Specifies the execution environment. If a `container_id` is found in the step's dependencies, `docker` will be automatically set to `true`.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Monitor Source Code" --settings '{
  "dynamic_lab": {
    "files": [
      "main.go",
      "go.mod"
    ]
  }
}'
```

### 7. `dynamic_rubric`

Parses a rubric file (in Markdown format) and generates child steps based on its content. This is highly useful for automated grading and dynamic task generation.

**Settings:**

```json
{
  "dynamic_rubric": {
    "file": "rubric.md",
    "environment": {
      "docker": true,
      "image_tag": "your-image:latest",
      "image_id": "sha256:abcdef..."
    }
  }
}
```

- `file`: The path to the Markdown file containing the rubric criteria.
- `environment`: If `docker` is `true`, this step will first create a `docker_run` step with the specified `image_tag` and `image_id`, and then generate `docker_shell` steps for each rubric criterion that depend on it.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Grade Project" --settings '{
  "dynamic_rubric": {
    "file": "rubric.md",
    "environment": {
      "docker": true,
      "image_tag": "grader:v1",
      "image_id": "sha256:123..."
    }
  }
}'
```

### 8. `docker_pull`

Pulls a Docker image from a registry.

**Settings:**

```json
{
  "docker_pull": {
    "image_tag": "nginx:latest",
    "image_id": "sha256:abcdef123456...", // Optional: For verification after pull
    "prevent_run_before": "2024-01-01T00:00:00Z", // Optional: RFC3339 timestamp
    "depends_on": [ // Optional
      { "id": 104 }
    ]
  }
}
```
- `image_tag`: The full tag of the Docker image to pull (e.g., `ubuntu:22.04`, `my-private-repo/my-image:v1.2.3`). This is required.
- `image_id` (optional): If provided, the system will verify that the pulled image's ID matches this value. If not provided, the system will fetch and store the pulled image's ID in the step results and settings.
- `prevent_run_before` (optional): An RFC3339 formatted timestamp (e.g., `2023-12-31T23:59:59Z`). If this timestamp is in the future, the step will be skipped until the specified time. After a successful pull, this field is automatically updated to 6 hours in the future to prevent rapid successive pulls.
- `depends_on` (optional): A list of other step IDs that must be completed successfully before this step can run.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Pull Nginx Image" --settings '{
  "docker_pull": {
    "image_tag": "nginx:latest"
  }
}'
```
