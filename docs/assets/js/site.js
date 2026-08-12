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
    stopped: {
      gates: {
        intent: ["pass", "armed", "Stop is not a success verdict"],
        binding: ["pass", "bound", "matching session · turn · workspace"],
        stop: ["pass", "observed", "same armed turn · not a continuation"],
        countdown: ["wait", "off", "dry-run never crosses the power boundary"],
      },
      title: "Lifecycle recorded",
      detail: "The exact Stop was observed; no power backend was called.",
      code: "DRY_RUN_COMPLETE",
      tone: "hold",
    },
    continued: {
      gates: {
        intent: ["pass", "armed", "dry-run lifecycle observation"],
        binding: ["pass", "bound", "matching session · turn · workspace"],
        stop: ["wait", "waiting", "the target resumed before Stop"],
        countdown: ["wait", "off", "no power authority was created"],
      },
      title: "Machine stays on",
      detail: "Continuing the conversation resets or cancels the observed lifecycle grant.",
      code: "ARMED",
      tone: "hold",
    },
    unsupported: {
      gates: {
        intent: ["blocked", "rejected", "execute readiness is checked before arm"],
        binding: ["wait", "none", "no execute job is created"],
        stop: ["wait", "none", "Hook observation cannot grant power"],
        countdown: ["blocked", "blocked", "trusted final arbitration is unavailable"],
      },
      title: "Machine stays on",
      detail: "The current public runtime rejects Stop-based execute on every platform.",
      code: "STOP_ARBITRATION_UNAVAILABLE",
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
