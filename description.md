# internal refused reads reported in status

some refused reads are reported in status and they are internal: no user
explicitly asked for anything they couldn't read, or at least apparently so.

after triggering some charter rules we see

```
% verse st
...
perms:
  refused:
    read: 22
...
```
