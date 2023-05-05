# packagedeployer

## Description
packagedeployer controller

## Usage

### Fetch the package
`kpt pkg get REPO_URI[.git]/PKG_PATH[@VERSION] packagedeployer`
Details: https://kpt.dev/reference/cli/pkg/get/

### View package content
`kpt pkg tree packagedeployer`
Details: https://kpt.dev/reference/cli/pkg/tree/

### Apply the package
```
kpt live init packagedeployer
kpt live apply packagedeployer --reconcile-timeout=2m --output=table
```
Details: https://kpt.dev/reference/cli/live/
