# task-sync

## Configuration

Task Sync reads its configuration from `~/.config/task/task.conf`. This file controls PASS/FAIL markers, timeouts, and the database connection. The `.env` file is now optional and only used as a fallback if settings are not present in `task.conf`.

Create the file and populate it like this:

```ini
# ~/.config/task/task.conf
PASS_MARKER=#__PASS__#
FAIL_MARKER=#__FAIL__#
TIMEOUT_MARKER=#__TIMEOUT__#
TIMEOUT_SECONDS=45

# Database (preferred over .env)
DB_HOST=your_database_host
DB_PORT=your_database_port
DB_USER=your_database_user
DB_PASSWORD=your_super_duper_password
DB_NAME=your_database_name
DB_SSL=false

# Alternatively, you can set a full DATABASE_URL:
# DATABASE_URL=postgres://user:pass@host:port/dbname?sslmode=disable
```

Notes:

- __Load order__: `DATABASE_URL` in `task.conf` is used if present; otherwise `DB_*` keys are used. If neither are present, environment variables (including optional `.env`) are used as a fallback.
- __SSL__: `DB_SSL` accepts `false`, `true` (maps to `require`), or an explicit `sslmode` (e.g., `disable`, `require`).
- __Timeout__: `TIMEOUT_SECONDS` controls rubric command hard timeouts.

## Task Commands

### Run All Steps for a Specific Task

To execute all steps for a specific task (by its numeric ID):

```bash
./task-sync task run <task_id>
```
- This command will process all steps associated with the given task, in ID order.
- Example:
  ```bash
  ./task-sync task run 3
  ```
- This is similar to the global `run-steps` command, but only processes steps for the specified task.

### Run All Pending Steps Globally

To process all pending steps for all tasks:

```bash
./task-sync run-steps
```


### Update an Existing Step (CLI examples)

Use `step edit` to update top-level fields or nested JSON settings using dot notation. Values are parsed as JSON when valid; otherwise treated as strings.

Examples:

```bash
# Update the step title (top-level field)
./task-sync step edit <step_id> --set title="Run App Container (prod)"

# Set a nested settings key (e.g., docker_run.parameters)
./task-sync step edit <step_id> --set docker_run.parameters='["-p","8080:80","--rm"]'

# Change a single string key
./task-sync step edit <step_id> --set docker_run.container_name=my-app

# Remove a settings key (dot notation supported)
./task-sync step edit <step_id> --remove-key docker_run.parameters
```



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

### Step Type Quick Reference

- **file_exists** — Assert presence of files. Fails if any are missing.
- **docker_pull** — Pull an image from a registry.
- **docker_build** — Build an image; uses file hash triggers to skip unchanged builds.
- **docker_run** — Start a container from an image; can keep running (keep-alive).
- **docker_pool** — Maintain a pool of running containers and record assignments in task settings.
- **docker_shell** — Run shell commands inside an existing running container.
- **docker_volume_pool** — Create/manage volumes and long-lived containers; set ownership/safe.directory for git.
- **docker_extract_volume** — Sync files from the original volume into solution and golden volumes.
- **model_task_check** — Generate/verify a model artifact; supports a `force` flag to bypass hash checks.
- **rubrics_import** — Import rubric data from JSON and/or reference Markdown for downstream steps.
- **rubric_set** — Parse rubric markdown, manage container assignments, and create/update `rubric_shell` steps.
- **rubric_shell** — For each criterion, clean repo, apply patches, run rubric command; results saved to `steps.results`.
- **dynamic_rubric** — Generate `rubric_shell` steps for solution↔container pairs across criteria; run by specific step only.

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

### 2. `docker_pull`

Pulls a Docker image from a registry.

**Settings:**

```json
{
  "docker_pull": {
    "image_id": "ubuntu",
    "image_tag": "latest",
    "prevent_run_before": "2024-01-01T00:00:00Z"
  }
}
```

### 3. `docker_build`

Builds a Docker image from a Dockerfile. It tracks file changes using hashes to avoid unnecessary rebuilds.

**Settings:**

```json
{
  "docker_build": {
    "image_id": "my-app",
    "image_tag": "latest",
    "files": {
        "main.go": "<hash_of_main.go>",
        "go.mod": "<hash_of_go.mod>",
        "Dockerfile": "<hash_of_Dockerfile>"
    },
    "parameters": ["--build-arg", "VERSION=1.0"],
    "depends_on": [
      { "id": 101 }
    ]
  }
}
```

### 4. `docker_run`

Runs a Docker container from a previously built or pulled image. The `image_id` and `image_tag` are inherited from its dependencies.

**Settings:**

```json
{
  "docker_run": {
    "container_name": "my-running-app",
    "parameters": ["-p", "8080:80"],
    "keep_forever": false,
    "depends_on": [
      { "id": 102 }
    ]
  }
}
```

### 5. `docker_pool`

Creates and manages a pool of running Docker containers from a specific image. This is useful for running tests in parallel.

**Settings:**

```json
{
  "docker_pool": {
    "pool_size": 3,
    "parameters": ["-v", "/data:/data"],
    "keep_forever": true,
    "depends_on": [
      { "id": 102 }
    ]
  }
}
```

### 6. `docker_shell`

Executes a series of shell commands inside a running Docker container. The container is determined by its dependencies.

**Settings:**

```json
{
    "docker_shell": {
        "command": [
            {
                "bash": "echo 'hello world'"
            },
            {
                "bash": "ls -la"
            }
        ],
        "depends_on": [
            {
                "id": 103
            }
        ]
    }
}
```

### 7. `rubrics_import`

Imports rubric definitions from a JSON file (or reads Markdown directly) for downstream steps. MHTML support has been removed.

**Settings:**

```json
{
  "rubrics_import": {
    "json_file": "path/to/rubrics.json",
    "md_file": "path/to/rubric.md",
    "depends_on": []
  }
}
```

### 8. `rubric_set`

Manages a set of rubrics and generates `rubric_shell` steps for each criterion. It assigns solution patches to specific containers for testing.

**Settings:**

```json
{
  "rubric_set": {
    "rubrics": ["path/to/rubric.md"],
    "hash": "<hash_of_rubric.md>",
    "assign_containers": {
      "solution1.patch": "container-1",
      "solution2.patch": "container-2"
    },
    "depends_on": [
      { "id": 104 }
    ]
  }
}
```

### 9. `rubric_shell`

Executes a test command against a specific rubric criterion inside one or more Docker containers. It iterates through the `assign_containers` map, applying each solution patch and running the command in the corresponding container.

**Settings:**

```json
{
  "rubric_shell": {
    "assign_containers": {
      "solution1.patch": "container-1",
      "solution2.patch": "container-2"
    },
    "command": "ansible-playbook -i inventory.ini test.yml",
    "criterion_id": "unique-criterion-uuid",
    "counter": "1",
    "score": 10,
    "required": true,
    "depends_on": [
      { "id": 105 }
    ]
  }
}
```

### 10. `dynamic_rubric`

A comprehensive step that dynamically generates `rubric_shell` steps based on rubric files and associated solution patches. It monitors files for changes and assigns containers for testing.

**Settings:**

```json
{
  "dynamic_rubric": {
    "rubrics": ["TASK_DATA.md"],
    "hash": "<hash_of_TASK_DATA.md>",
    "files": {
        "file1.txt": "<hash_of_file1.txt>"
    },
    "assign_containers": {
      "solution1.patch": "container-1",
      "solution2.patch": "container-2"
    },
    "environment": {
        "docker": true,
        "image_id": "my-image",
        "image_tag": "latest"
    },
    "depends_on": [
      { "id": 106 }
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
    "keep_forever": true,
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
- `keep_forever` (optional): A boolean flag. If set to `true`, the system will ensure the container keeps running, even if no long-running command is specified in `parameters`. This is useful for services that need to be persistently available.
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

### 5. `rubrics_import`

Parses a rubric JSON file and/or reads an existing Markdown rubric. Use this to register rubric criteria and compute hashes for downstream steps. MHTML support has been removed.

**Settings:**

```json
{
  "rubrics_import": {
    "json_file": "rubrics.json",
    "md_file": "TASK_DATA.md"
  }
}
```

- `json_file`: Path to the rubric JSON (array of rubric items).
- `md_file`: Optional path to a Markdown rubric file used by other steps.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Import Rubric" --settings '{
  "rubrics_import": {
    "json_file": "path/to/your/rubrics.json",
    "md_file": "TASK_DATA.md"
  }
}'
```

### 6. `dynamic_rubric`

Dynamically generates and manages a set of `rubric_shell` steps based on one or more rubric files and a set of solution patches. This step is designed to work with a `docker_pool` to test multiple solutions against rubric criteria in parallel.

**Settings:**

```json
{
  "dynamic_rubric": {
    "rubrics": ["NEW_TASK_DATA.md"],
    "files": {
      "solution1.patch": "",
      "solution2.patch": ""
    },
    "hashes": {
      "NEW_TASK_DATA.md": "<hash>",
      "solution1.patch": "<hash>",
      "solution2.patch": "<hash>"
    },
    "assign_containers": {
      "solution1.patch": "container_name_1",
      "solution2.patch": "container_name_2"
    },
    "depends_on": [{ "id": 126 }]
  }
}
```

- `rubrics` (required): A list of paths to the Markdown files containing rubric criteria.
- `files` (optional): A map of associated file paths (like solution patches) to track for changes. The value can be empty.
- `hashes` (output): A map of file paths to their SHA256 hashes. This is used for change detection and is managed automatically by the system.
- `assign_containers` (required): A map that pairs a file (e.g., a solution patch) with a container name from a `docker_pool` step. This tells the system which container to use for testing each solution.
- `depends_on` (required): This step must depend on a `docker_pool` step that provides the containers specified in `assign_containers`.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Create Dynamic Rubrics" --settings '{
  "dynamic_rubric": {
    "rubrics": ["NEW_TASK_DATA.md"],
    "files": {
      "solution1.patch": "",
      "solution2.patch": ""
    },
    "assign_containers": {
      "solution1.patch": "worker_pool_container_1",
      "solution2.patch": "worker_pool_container_2"
    },
    "depends_on": [{ "id": 126 }]
  }
}'
```

### 7. `docker_pull`

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

### 8. `docker_pool`

Manages a pool of identical Docker containers, ensuring a specified number of instances are running. This is useful for creating a set of workers or services that can be used by other steps.

**Settings:**

```json
{
  "docker_pool": {
    "image_tag": "my-worker-app:latest",
    "image_id": "sha256:...",
    "pool_size": 3,
    "parameters": ["--network", "my-net"],
    "keep_forever": true,
    "depends_on": [
      { "id": 102 }
    ],
    "containers": [
        { "container_id": "abc...", "container_name": "worker_1" },
        { "container_id": "def...", "container_name": "worker_2" },
        { "container_id": "ghi...", "container_name": "worker_3" }
    ]
  }
}
```

- `image_tag` (required): The Docker image to use for the containers in the pool.
- `image_id` (optional): The specific image ID to use. If provided by a dependency (like a `docker_build` step), it will be used.
- `pool_size` (required): The desired number of containers to maintain in the pool.
- `parameters` (optional): A list of extra parameters to pass to the `docker run` command for each container.
- `keep_forever` (optional): A boolean flag. If set to `true`, the system will ensure the containers keep running, even if no long-running command is specified in `parameters`.
- `depends_on` (optional): This step will typically depend on a `docker_build` or `docker_pull` step.
- `containers` (output): A list of objects, each containing the `container_id` and `container_name` of a container in the pool. This field is populated automatically.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Create Worker Pool" --settings '{
  "docker_pool": {
    "image_tag": "my-worker:1.0",
    "pool_size": 4,
    "keep_forever": true,
    "parameters": ["--memory=256m"],
    "depends_on": [{ "id": 7 }]
  }
}'

### 9. `rubric_set`

Parses a rubric Markdown file and dynamically creates `rubric_shell` steps for each criterion. This step tracks the main rubric file, a held-out test, and up to four solution files for changes.

**Settings:**

```json
{
  "rubric_set": {
    "file": "TASK_DATA.md",
    "held_out_test": "held_out_test.patch",
    "solution_1": "solution1.patch",
    "solution_2": "solution2.patch",
    "hashes": {
      "file": "<hash>",
      "held_out_test": "<hash>",
      "solution_1": "<hash>",
      "solution_2": "<hash>"
    },
    "depends_on": [
      { "id": 144 }
    ]
  }
}
```

- `file` (required): The path to the Markdown file containing rubric criteria.
- `held_out_test` (optional): Path to a patch file for a held-out test.
- `solution_1` to `solution_4` (optional): Paths to patch files for different solutions.
- `hashes` (output): A map of file paths to their SHA256 hashes, used for change detection and managed automatically.
- `depends_on` (optional): A list of other step IDs that must complete before this step runs. Typically depends on a `rubrics_import` step.

**Example CLI Command:**

```bash
./task-sync step create --task-id 1 --title "Set Up Rubric Steps" --settings '{
  "rubric_set": {
    "file": "TASK_DATA.md",
    "held_out_test": "held_out_test.patch",
    "solution_1": "solution1.patch"
  }
}'
```

### 10. `rubric_shell`

Executes a test command for a single rubric criterion. This step is generated automatically by a parent step (like `dynamic_rubric` or `rubric_set`) and is not typically created manually. When generated by a `dynamic_rubric` step, it runs the test against multiple solution patches, each within a dedicated container from a `docker_pool`.

**Settings (Populated by Generator Step):**

```json
{
  "rubric_shell": {
    "command": "/app/run_criterion_test.sh",
    "criterion_id": "d0aba505-cc93-489c-bc8b-da566a1f0af5",
    "counter": "1",
    "score": 10,
    "required": true,
    "assign_containers": {
      "solution1.patch": "container_name_1",
      "solution2.patch": "container_name_2"
    },
    "generated_by": "127",
    "depends_on": [{ "id": 127 }]
  }
}
```

- `command`: The shell command to execute, taken from the criterion's `held_out_test` field in the rubric file.
- `criterion_id`, `counter`, `score`, `required`: Details about the specific rubric criterion being tested.
- `assign_containers`: A map of solution files to container names, inherited from the parent `dynamic_rubric` step.
- `generated_by`: The ID of the parent step that created this step.
- `depends_on`: A dependency on the parent step.

## Rubric Import Logic Update

As of the latest changes, the rubric import system now supports importing from a JSON file (rubrics.json) in addition to the traditional markdown file. When rubrics.json is present in the task directory, it is prioritized, and the markdown file is ignored. The JSON structure is mapped to the Criterion model as follows:
- Counter: Generated from array index (starting at 1)
- Title: From `rubricItemId`
- Rubric: From `criterion`
- HeldOutTest: From `forms.criterion_test_command`
- Score and Required: Directly from their respective fields

This change improves flexibility and allows for easier rubric definition.
