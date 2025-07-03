# TASK DATA 3

### crit-01: 12034e7e-fc2d-4aed-a4c6-a15303ab534f

**Score**: 10
**Required**: true
**Criterion**:
Did the solution removed imports related to threading, and multiprocessing?

**Held-out tests**:
```bash
/app/ansible/held_out_tests/detect_multiprocessing_imports.py -v --start-folder /app/ansible/ --skip-folder held_out_tests --forbidden threading,multiprocessing
```



### crit-02: 91a8f96a-7ea2-4574-8ab3-1c43e8344715

**Score**: 10
**Required**: true
**Criterion**:
Has usage of Threading been refactored?

**Held-out tests**:
```bash
/app/ansible/held_out_tests/detect_threading_usage.py -v --start-folder /app/ansible
```
