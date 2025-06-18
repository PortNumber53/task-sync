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
| `status`     | `TEXT`      | The current status (e.g., 'new', 'active', 'completed', 'failed').          |
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

Builds a Docker image from a Dockerfile.

**Settings:**

```json
{
  "docker_build": {
    "image_tag": "my-custom-app:latest",
    "files": [
      "Dockerfile",
      "main.go",
      "go.mod",
      "go.sum"
    ],
    "shell": [
      "echo 'Starting the build...'"
    ],
    "depends_on": [
      { "id": 101 }
    ]
  }
}
```
- `image_tag`: The tag to apply to the built image.
- `files`: A list of files to monitor. The step will re-run if their hashes change.
- `shell` (optional): A list of shell commands to execute before the build.
- `depends_on` (optional): A list of other step IDs that must be completed successfully before this step can run.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Build App Image" --settings '{
  "docker_build": {
    "image_tag": "my-app:1.0",
    "files": ["Dockerfile", "app/"]
  }
}'
```

### 3. `docker_run`

Runs a command in a new Docker container.

**Settings:**

```json
{
  "docker_run": {
    "image_tag": "my-custom-app:latest",
    "command": [
      "./my_app", "--verbose"
    ],
    "depends_on": [
      { "id": 102 }
    ]
  }
}
```
- `image_tag`: The Docker image to use for the container.
- `command`: The command and arguments to execute inside the container.
- `depends_on`: This step will typically depend on a `docker_build` step.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Run App Container" --settings '{
  "docker_run": {
    "image_tag": "my-app:1.0",
    "command": ["./run.sh"],
    "depends_on": [{ "id": 1 }]
  }
}'
```

### 4. `docker_shell`

Executes a shell command inside a specified Docker container or image.

**Settings:**

```json
{
  "docker_shell": {
    "docker": {
      "image_tag": "ubuntu:latest"
    },
    "command": [
      {
        "command": "ls -la /app"
      },
      {
        "command": "cat /app/config.yml"
      }
    ],
    "depends_on": [
      { "id": 103 }
    ]
  }
}
```
- `docker`: Specifies the target. Use `image_tag` for a new container or `container_id`/`container_name` for an existing one.
- `command`: A list of command objects to execute sequentially.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Inspect Container" --settings '{
  "docker_shell": {
    "docker": {
      "image_tag": "my-app:1.0"
    },
    "command": [
      { "command": "ls /" }
    ],
    "depends_on": [{ "id": 2 }]
  }
}'
```

### 5. `docker_rubrics`

This step appears to be a specialized version of `docker_build`, likely for automated grading or validation tasks where code is evaluated against a set of rules inside a Docker environment.

**Settings:**

```json
{
  "docker_rubrics": {
    "image_tag": "grader-env:v2",
    "files": [
      "student_submission.py",
      "tests/"
    ],
    "depends_on": []
  }
}
```

**Example CLI Command:**

```bash
./task-sync step create --task-id 2 --title "Grade Submission" --settings '{
  "docker_rubrics": {
    "image_tag": "python-grader:latest",
    "files": ["submission/"]
  }
}'
```
