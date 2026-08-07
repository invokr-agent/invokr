# 16 — Diagrams

The same ground as guides 01–15, drawn. Ordered from the outside world down to
subsystem internals. All names are real packages/types; if a diagram and the
code disagree, the code wins — fix the diagram.

View these rendered: `make diagram-view` (generates an HTML page and opens it
in your browser).

## 1. System context — Invokr and the outside world

```mermaid
flowchart LR
    subgraph users["People / triggers"]
        term["Terminal user"]
        script["Scripts / CI"]
        cron["OS crontab"]
        chat["Telegram / Signal users"]
    end

    subgraph invokr["invokr-agent (one Go binary)"]
        tui["agent (TUI / plain)"]
        exec["exec / exec resume"]
        jobsrun["jobs run"]
        serve["chat serve"]
    end

    subgraph external["External systems"]
        llm["OpenAI-compatible provider\n(llama.cpp, vLLM, ...)"]
        hf["HuggingFace\n(lazy GGUF downloads)"]
        pypi["PyPI\n(via allowlist proxy)"]
        docker["docker / podman"]
        mcp["MCP servers (stdio)"]
        search["Brave / Tavily / custom HTTP"]
    end

    home[("~/.invokr\nconfig.json · memory.db\nsessions/*.jsonl · toolbox/ · skills/\nagents/ · AGENTS/USER/PROJECT.md")]

    term --> tui
    script --> exec
    cron --> jobsrun
    chat --> serve

    tui & exec & jobsrun & serve --> llm
    invokr --> home
    invokr --> hf
    invokr --> pypi
    invokr --> docker
    invokr --> mcp
    invokr --> search
```

## 2. Layer map — who may import whom

Arrows point at dependencies. `capability/*` never imports `platform/runtime`
(sole sanctioned exception: `capability/jobs/jobrun`, which exists to hold that
import without a cycle).

```mermaid
flowchart TD
    main["main.go"] --> cmd["cmd/ + cmd/agentui/"]
    cmd --> runtime["platform/runtime\n(composition root)"]

    runtime --> agent["agent/\nSession · Runner · Gateway · TurnProcessor"]
    runtime --> tools["tools/ (Caller)\ntools/system/*"]
    runtime --> caps["capability/*\nmemory · approval · skills · syntax\nagentdef · jobs · sandbox · sessionstore"]
    runtime --> clients["clients/"]
    runtime --> msg["surface/messaging"]

    agent --> clients
    agent --> caps
    agent --> toolscore["tools/core"]
    tools --> caps
    tools --> toolscore

    agent & tools & caps & msg --> platform["platform/events · platform/turn"]
    agent & tools & caps & clients & msg --> leaves["config · constants · support/fsutil"]

    jobrun["capability/jobs/jobrun"] -.->|"only exception"| runtime
```

## 3. Bootstrap — what `platform/runtime.Bootstrap` wires

```mermaid
flowchart TD
    cfg["config.AppConfig\n(defaults → file → env → flags)"] --> boot["Bootstrap()"]
    boot --> log["zap logger (+ optional TUI ring buffer)"]
    boot --> prov["clients.Client"]
    prov -.->|"background goroutines"| meta["/props → context window\n/v1/models → model fallback (ADR 0006)"]
    boot --> bus["events.OrderedDispatcher"]
    boot --> brok["approval.Broker"]
    boot --> zw["zerfoo.Worker (opt, lazy)"]
    boot --> mem["memory.TieredManager\n(chat summarizer wired)"]
    zw --> mem
    boot --> hist["history.Store (memory.db)"]
    boot --> skills["skills.FilesystemManager"]
    boot --> defs["agentdef.Registry"]
    boot --> caller["tools.Caller"]
    skills & defs & mem & brok & bus --> caller
    caller --> sys["tools/system/* natives"]
    caller --> tb["toolbox plugins (source: toolbox)"]
    caller --> mcps["MCP tools (source: mcp:name)"]
    boot --> gate["recallgate.Gate (opt)"]
    boot --> sess["agent.NewSession"]
    caller & mem & gate & bus & prov --> sess
    boot --> cmds["CommandService (slash commands)\n+ NewChatSession factory"]
    mem -->|"Register(curator handler)"| bus
```

## 4. One turn — sequence

```mermaid
sequenceDiagram
    actor U as User
    participant UI as agentui / exec
    participant R as agent.Runner
    participant B as contextBudget
    participant G as OpenAIModelGateway
    participant P as agentprompt.Builder
    participant C as clients.Client
    participant T as TurnProcessor
    participant K as tools.Caller
    participant E as events bus

    U->>UI: message
    UI->>R: Session.Send(ctx, text)
    loop up to agent.max_turns (40)
        R->>B: Prepare(messages)
        Note over B: 70% → warn + prefetch summary<br/>85% → compact (ADR 0003)
        R->>G: Begin (turn 0) / Continue
        G->>P: Build system prompt (turn 0)
        P-->>G: base + syntax + skills + tools + fenced memory
        G->>C: chat completion (stream)
        C-->>UI: reply deltas (plain text only)
        C-->>G: completion
        Note over G: retry ×1: tool-call parse 500<br/>retry ×1: empty completion
        G-->>R: ModelTurn
        R->>T: Process(completion)
        alt tool calls
            T->>K: HandleCall (parallel ≤4, ordered results)
            K-->>T: payload / flagged error / denial
            T-->>E: tool_started / completed / failed
        else final reply
            T-->>R: done
        end
        Note over R: repeat guard: 3 identical calls → nudge
    end
    R-->>E: after_response (→ memory curator)
    R-->>UI: AgentResponse + outcome ledger (ADR 0001)
```

## 5. Tool dispatch — inside `Caller.HandleCall` and `core.Tool[A]`

```mermaid
flowchart TD
    call["ToolCall: name + arguments"] --> guard["panic recovery"]
    guard --> vis{"schema loaded for session\nor AlwaysVisible?"}
    vis -- no --> rej["error: call tool_search\ninclude_schema=true first"]
    vis -- yes --> h["native handler = core.Tool.Handle"]

    subgraph lifecycle["core.Tool lifecycle"]
        h --> dec["Decode args"] --> prep["Prepare (validate/normalize)"] --> appr{"Approve?"}
        appr -- "Auto()" --> ex["Execute"]
        appr -- "Ask(req)" --> brok["approval.Broker → user"]
        brok -- approved --> ex
        brok -- denied --> den["approval.DeniedError\n(remembered for this request)"]
        appr -- "Deny(reason)" --> den
    end

    ex --> res{"result"}
    res -- ok --> filt["tokf result filter"]
    res -- "ToolError with Payload" --> flag["completed-but-flagged:\nmodel sees Payload, UI shows Code"]
    res -- error --> hard["hard failure → ToolOutcome"]
    flag --> filt
    filt --> model["payload → model + ledger"]
    den --> model
```

## 6. Memory — write and read paths

```mermaid
flowchart TD
    subgraph write["Write path (event-driven)"]
        ar["after_response event"] --> cur["curator.HandleEvent\n(skips subagent turns)"]
        cur --> raw["raw_memory_events (sqlite)\n+ mirror to sessions/*.jsonl"]
        raw -->|"every 12 events"| q["bounded worker queue (8)"]
        q --> sum["chat-model summarizer\nJSON summary + facts"]
        sum --> recs["memory_records\n(summary + fact rows)"]
        recs --> emb["zerfoo embed → memory_vectors"]
        cur --> sweep["forgetting sweep (opt):\narchive < min_retention · purge · prune"]
    end
    subgraph pinwrite["Pinned markdown (explicit only)"]
        tool["memory tool\n(source: markdown)"] --> pinned["USER.md / PROJECT.md"]
    end

    subgraph read["Read paths"]
        pb["PromptBlocks (turn 0)"] --> pin["pinned markdown blocks"] --> budget["6000-char budget\n+ recalled-memory fence"]
        rc["Recall(query)"] --> orch["recall.Orchestrator"]
        orch --> vec["vector (zerfoo + cosine)\nor FTS5 bm25"]
        orch --> pinrec["pinned markdown records"]
        orch -->|"no hits"| fb["raw-event fallback\n(session, else cross-session ≤30d, score 0.1)"]
        vec & pinrec & fb --> rr["retention re-rank + reinforce (opt)"]
    end

    gate["recallgate (opt, first turn):\nprobe → queries → Recall → 'Relevant Memory' block"] --> rc
```

## 7. Approvals — one broker, four answerers

```mermaid
flowchart LR
    tool["Tool Approve → core.Ask(request)"] --> brok["approval.Broker\n(blocking rendezvous)"]

    brok --> q["TUI: ApprovalQueue\na/d per item · A/D all · v details\n/mode manual · auto · plan"]
    brok --> ex["exec: policy from flags\ndeny | --allow tier | --allow-all"]
    brok --> jb["jobs: fixed allow/deny per job\n(schedule-tool jobs always deny)"]
    brok --> ch["chat: Telegram buttons /\nSignal YES id · NO id (2 min → deny)"]

    q & ex & jb & ch --> dec["Decision"]
    dec -->|approved| run["Execute"]
    dec -->|denied| de["DeniedError → model payload\nidentical retry auto-refused this request"]
```

## 8. Toolbox invocation — sandbox and supply chain

```mermaid
flowchart TD
    inv["plugin call"] --> ap["policy.approval ≠ auto → ask user"]
    ap --> py{"python with\nrequirements.txt?"}
    py -- yes --> venv["per-tool .venv\nsha256 marker up to date?"]
    venv -- stale --> sc["supply chain:\npip --dry-run --report via allowlist proxy\n→ validate name/version/sha256/host\n→ release age ≥ 14d → user approves list\n→ hash-locked wheel download → --no-index install"]
    py -- no --> sb
    venv -- fresh --> sb
    sc --> sb{"user namespaces\navailable? (Linux)"}
    sb -- yes --> ns["unshare --user --mount --pid ...\nRO binds: /usr /bin /lib toolDir interpreter\nRW: workspace (if declared) · GOCACHE\nno --net unless declared · pivot_root . .\nulimit -v · 60s wall clock"]
    sb -- no --> optout{"INVOKR_TOOLBOX_\nALLOW_UNSANDBOXED=1?"}
    optout -- no --> refuse["refused (no silent fallback)"]
    optout -- yes --> host["host process (warned)"]
    ns & host --> io["stdin: JSON call envelope\nstdout: text or {result, error}\n(daemon: NDJSON per call)"]
```

## 9. TUI threading — the one rule

```mermaid
flowchart LR
    subgraph anyG["any goroutine (bus handlers)"]
        ev["tool / notice / delta events"] --> push["toolEventQueue.Push\n(mutex-guarded slice)"]
        push --> wake["program.Send(toolEventsReadyMsg)"]
    end
    subgraph teaG["Bubble Tea goroutine (single-threaded)"]
        wake --> upd["model.Update"] --> drain["toolEventQueue.Drain"] --> log2["chatLog.ApplyEvent\n(mutate tool block in place)"] --> vp["viewport re-render\n(follow bottom unless user scrolled)"]
    end
    style anyG fill:none
    style teaG fill:none
```

Never touch model state from a handler goroutine — push and wake.

## 10. Subagents — delegate_task

```mermaid
flowchart TD
    dt["delegate_task (1–6 tasks)"] --> ap["approval (always)"]
    ap --> sem["semaphore max_parallel ≤ 6\ndepth cap 1 (no re-delegation)"]
    sem --> run["subagentRunner.RunSubagent"]
    run --> def{"task.agent named?"}
    def -- yes --> reg["agentdef.Registry\nmodel · max_turns · instructions\n· tool allowlist"]
    reg --> fc["filteredCaller wraps tools\n(dynamic registration = no-op)"]
    def -- no --> full["full tool set"]
    fc & full --> child["agent.NewSession\nmemory = NoopManager\nParentSessionID set"]
    child --> res["SubagentResult\n{label, final_text, stop_reason, usage}"]
    res --> memrec["parent records episode\nvia recordDelegateMemory"]
    res --> out["only summaries return to parent"]
```
