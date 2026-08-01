(() => {
  const header = document.querySelector("[data-site-header]");
  const navToggle = document.querySelector("[data-nav-toggle]");
  const siteNav = document.querySelector("[data-site-nav]");

  const updateHeader = () => {
    header?.classList.toggle("is-scrolled", window.scrollY > 12);
  };

  updateHeader();
  window.addEventListener("scroll", updateHeader, { passive: true });

  const closeNavigation = () => {
    if (!navToggle || !siteNav) return;
    navToggle.setAttribute("aria-expanded", "false");
    siteNav.classList.remove("is-open");
  };

  navToggle?.addEventListener("click", () => {
    if (!siteNav) return;
    const isOpen = navToggle.getAttribute("aria-expanded") === "true";
    navToggle.setAttribute("aria-expanded", String(!isOpen));
    siteNav.classList.toggle("is-open", !isOpen);
  });

  siteNav?.querySelectorAll("a").forEach((link) => {
    link.addEventListener("click", closeNavigation);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeNavigation();
      navToggle?.focus();
    }
  });

  const scenarios = {
    complete: {
      gates: {
        intent: ["pass", "verified", "typed arm request"],
        binding: ["pass", "bound", "matching lifecycle identity"],
        evidence: ["pass", "valid", "checks passed · no remaining work"],
        authority: ["hold", "hold", "same-host live proof required"],
      },
      title: "Machine stays on",
      detail: "Code is present; current release evidence cannot authorize execution.",
      code: "HOST_AUTHORITY_UNAVAILABLE",
      tone: "hold",
    },
    partial: {
      gates: {
        intent: ["pass", "verified", "typed arm request"],
        binding: ["pass", "bound", "matching lifecycle identity"],
        evidence: ["blocked", "rejected", "failed checks or remaining work"],
        authority: ["blocked", "blocked", "completion gate did not pass"],
      },
      title: "Machine stays on",
      detail: "Partial work is never promoted to successful completion.",
      code: "VERIFICATION_FAILED",
      tone: "blocked",
    },
    approval: {
      gates: {
        intent: ["pass", "verified", "typed arm request"],
        binding: ["pass", "bound", "matching lifecycle identity"],
        evidence: ["wait", "paused", "agent is waiting for user approval"],
        authority: ["blocked", "blocked", "approval-waiting state is not done"],
      },
      title: "Machine stays on",
      detail: "Waiting for a person pauses the job and withholds authority.",
      code: "PAUSED",
      tone: "blocked",
    },
  };

  const interlock = document.querySelector("[data-interlock]");
  const scenarioButtons = interlock?.querySelectorAll("[data-scenario]") ?? [];
  const result = interlock?.querySelector("[data-result]");

  const applyScenario = (name) => {
    const scenario = scenarios[name];
    if (!scenario || !interlock || !result) return;

    scenarioButtons.forEach((button) => {
      const active = button.dataset.scenario === name;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });

    Object.entries(scenario.gates).forEach(([gateName, values], index) => {
      const row = interlock.querySelector(`[data-gate="${gateName}"]`);
      if (!row) return;

      const [stateClass, stateLabel, detail] = values;
      row.classList.remove("is-pass", "is-hold", "is-wait", "is-blocked");
      row.classList.add(`is-${stateClass}`, "is-changing");
      row.querySelector(".gate-state").textContent = stateLabel;
      row.querySelector(".gate-copy small").textContent = detail;

      window.setTimeout(() => row.classList.remove("is-changing"), 90 + index * 55);
    });

    result.dataset.resultTone = scenario.tone;
    result.querySelector("[data-result-title]").textContent = scenario.title;
    result.querySelector("[data-result-detail]").textContent = scenario.detail;
    result.querySelector("[data-result-code]").textContent = scenario.code;
  };

  scenarioButtons.forEach((button) => {
    button.addEventListener("click", () => applyScenario(button.dataset.scenario));
  });

  document.querySelectorAll("[data-copy-button]").forEach((button) => {
    button.addEventListener("click", async () => {
      const target = document.getElementById(button.dataset.copyTarget);
      if (!target) return;

      const text = target.innerText;
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = text;
        textarea.setAttribute("readonly", "");
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
      }

      const previous = button.textContent;
      button.textContent = "Copied";
      button.classList.add("is-copied");
      window.setTimeout(() => {
        button.textContent = previous;
        button.classList.remove("is-copied");
      }, 1600);
    });
  });
})();
