# status: a store refusing writes is invisible

A store that stops accepting writes leaves a finished task reading 'running' forever and a gate on no docket. Dropping rather than stalling the loop is the right policy; the silence is the defect. The minimum is that the failure is countable and visible on verse status, since by construction it cannot be written to the store — api.Status carries no such counter today.

P5 of an earlier census, from docs/working/work/seam-repair.md.