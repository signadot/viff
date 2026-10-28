# The data grammar's errors name the CLI's flags, and every one is a usage error

Moving the data grammar into api (3f70ec41a) wrapped all of its errors as usage errors in the CLI, so a value that does not parse (--put, --expect, --grants, --if, on data and watch) prints the command's usage and exits with the usage code, where it printed the parse error; and a bad payload reads 'data --put: --put: …'. The verse system's data tools read the same errors naming the CLI's flags (put, if-at) rather than their own parameters (value, if_at).

Fix: api's errors say which field they are about and whether they are a misuse (api.FieldError), named as verse mcp's parameters are; the CLI says each by its flag, a usage error only for a misuse; the tools say each by its parameter.