# trek

**This project has moved to [github.com/printeers/trek](https://github.com/printeers/trek).**

## Requirements
At least version 13 of postgres is needed.

## Installation
`go install .`

## Setup
Create `trek.yaml`:
```yaml
model_name: <model_name>
db_name: <db_name>
db_users:
  - <db_user_1>
  - <db_user_2>
```

Create `<model_name>.dbm` using pgModeler.

## Creating migrations

`trek generate some-migration`

Use the `--dev` flag to continuously watch for file changes.

## Applying the migrations

Take a look at the `example/` directory.
