<script setup>
import { Check } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const { t, tasks } = useAppContext()
</script>

<template>
<section class="content-page">
          <div class="page-heading"><div><h2>{{ t.tasksTitle }}</h2><p>{{ t.tasksIntro }}</p></div><Check :size="28" /></div>
          <div class="task-table">
            <div v-for="task in tasks" :key="task.id" class="task-row"><Check :size="16" /><span>{{ task.title }}</span><time>{{ new Date(task.createdAt).toLocaleTimeString() }}</time></div>
            <div v-if="!tasks.length" class="empty-state">{{ t.noTasks }}</div>
          </div>
        </section>
</template>
