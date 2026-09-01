<script setup>
import { Maximize2, Minus, X } from 'lucide-vue-next'

import logo from '../../assets/logo.png?no-inline'
import { closeWindow, minimise, toggleMaximise } from '../../backend'

defineProps({
  running: { type: Boolean, default: false },
  runningLabel: { type: String, default: '' }
})
</script>

<template>
  <header class="titlebar">
    <div class="titlebar__drag">
      <img class="brand-mark" :src="logo" alt="CodingTo" />
      <span class="brand-name">CodingTo</span>
      <span v-if="running" class="run-pill"><span></span>{{ runningLabel }}</span>
    </div>
    <div class="window-controls">
      <button @click="minimise" aria-label="Minimize"><Minus :size="15" /></button>
      <button @click="toggleMaximise" aria-label="Maximize"><Maximize2 :size="13" /></button>
      <button class="window-close" @click="closeWindow" aria-label="Close"><X :size="16" /></button>
    </div>
  </header>
</template>
