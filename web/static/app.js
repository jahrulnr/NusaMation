/* NusaMation web control center — vanilla JS modules, no build step. */
import { bindEvents, syncAppHeight } from "./components/events.js?v=35";
import { boot } from "./components/boot.js?v=35";

bindEvents();
syncAppHeight();
boot();
