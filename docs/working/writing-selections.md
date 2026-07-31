# writing selections

a charter can condition on state outside $subject with `when:` and `on:`,
but it cannot copy data.

consider adding

```tony
- name: urgent-label
  stratum: observe
  condition:
    subject: { system: gh, kind: issue, value: { label: urgent } }
  action:
    patch: { triaged: true }
    derive:
      signadot:
        feature:
          priority: urgent
```
