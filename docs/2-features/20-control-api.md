# Control API

The server can be queried and controlled with an API, that can be enabled by toggling the `api` parameter in the configuration:

```yml
api: yes
```

To obtain a list of active paths, run:

```
curl http://127.0.0.1:9997/v3/paths/list
```

The control API is documented in the [Control API Reference page](../6-references/2-control-api.md) and in the [OpenAPI / Swagger file](https://github.com/bluenviron/mediamtx/blob/{version_tag}/api/openapi.yaml).

Be aware that by default the Control API is accessible by localhost only; to increase visibility or enforce authentication, check [Authentication](05-authentication.md).

## Persistence of changes

Changes made to the configuration through the API (and through the web UI, that uses the API) are saved in a file placed next to the configuration file, with the same name and the `.api.json` extension (`mediamtx.yml` -> `mediamtx.api.json`). The file is applied on top of the configuration file at startup and whenever the configuration file is reloaded, therefore changes survive restarts, while the configuration file is never modified.

To discard every change made through the API, stop the server and delete the file. Changes to internal users performed through the `/v3/config/internal-users` endpoints are not saved. Nothing is saved when the server is started without a configuration file.
