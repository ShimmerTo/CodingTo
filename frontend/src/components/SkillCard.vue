<script setup>
import { Sparkles } from 'lucide-vue-next'

defineProps({
  skill: { type: Object, required: true },
})
</script>

<template>
  <div class="skill-card__body">
    <header>
      <span class="skill-card__title">
        <h3 :title="skill.path" class="skill-card__name">{{ skill.name }}</h3>
        <span class="skill-source" :class="skill.sourceType === 'pi' ? 'is-pi' : 'is-managed'">{{ skill.sourceType === 'pi' ? 'pi' : (skill.loadMode === 'skills_list' ? 'skills_list' : 'ZIP / URL') }}</span>
      </span>
      <slot name="actions" />
    </header>
    <p>{{ skill.description }}</p>
    <slot />
  </div>
</template>
