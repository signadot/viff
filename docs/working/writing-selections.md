# writing selections

a charter can condition on state outside $subject with `when:` and `on:`,
but it cannot copy data.

consider adding

```tony
- name: urgent-label
  stratum: observe
  condition:
    subject: { system: gh, kind: issue, value: { labels: { urgent: null, linear: null } }
  action:
    patch: {

          
          
        
    }
    derive:
      system: signadot
      kind: feature
      id: $subject.value.labels.linear
      priority: urgent
```
